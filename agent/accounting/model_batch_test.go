package accounting

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
)

func modelBatchFixture(t *testing.T) (*Authority, []orchestrator.DispatchIntent) {
	t.Helper()
	a, original, _ := preparedActionFixture(t)
	ctx := context.Background()
	agg, err := a.LoadAggregate(ctx, *original.Job)
	if err != nil {
		t.Fatal(err)
	}
	agg.Source.Diff = &review.StructuredDiff{Files: []review.FileDiff{{Path: "service.go", Patch: "+change"}}}
	ctx, err = review.WithModelCheckpoint(review.WithModelValidation(ctx, &review.ModelValidation{Version: 1, MinConfidence: 0.45}), &review.Context{Source: agg.Source}, review.ModelCursor{Phase: review.ModelReviewBatches, BatchCount: 2})
	if err != nil {
		t.Fatal(err)
	}
	var intents []orchestrator.DispatchIntent
	for index := 0; index < 2; index++ {
		i := original
		i.ActionID = fmt.Sprintf("batch-%d", index)
		i.Step = fmt.Sprintf("step5_batch%d", index+1)
		i.Invocation = "complete"
		batchCtx, err := review.WithModelBatch(ctx, index, 2)
		if err != nil {
			t.Fatal(err)
		}
		i.ModelCheckpoint = review.ModelCheckpointFromContext(batchCtx)
		intents = append(intents, i)
	}
	return a, intents
}

func assertPreparedBatchCounts(t *testing.T, a *Authority, want int) {
	t.Helper()
	var actions, holds, owned, sending int
	err := a.db.QueryRow(`SELECT (SELECT count(*) FROM budget_actions),(SELECT count(*) FROM budget_holds),(SELECT count(*) FROM budget_action_leases WHERE worker_id IS NOT NULL),(SELECT count(*) FROM budget_actions WHERE state<>'prepared')`).Scan(&actions, &holds, &owned, &sending)
	if err != nil {
		t.Fatal(err)
	}
	if actions != want || holds != 3*want || owned != 0 || sending != 0 {
		t.Fatalf("partial or dispatchable batch: actions=%d holds=%d owned=%d sending=%d", actions, holds, owned, sending)
	}
}

func TestModelBatchPreparationIsAtomicAndRestartIdempotent(t *testing.T) {
	a, intents := modelBatchFixture(t)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			receipts, err := a.PrepareModelBatch(context.Background(), intents)
			if err == nil && (len(receipts) != 2 || receipts[0].ID != intents[0].ActionID || receipts[1].ID != intents[1].ActionID || receipts[0].AlreadyStarted || receipts[1].AlreadyStarted) {
				err = fmt.Errorf("invalid batch receipts: %#v", receipts)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertPreparedBatchCounts(t, a, 2)
	reopened := ledgerAuthority(t, a.db, ledgerLimits())
	if _, err := reopened.PrepareModelBatch(context.Background(), intents); err != nil {
		t.Fatal(err)
	}
	assertPreparedBatchCounts(t, a, 2)
}

func TestModelBatchPreparationRollsBackEverySlotAndHold(t *testing.T) {
	for _, fault := range []string{"later_route", "capacity", "commit"} {
		t.Run(fault, func(t *testing.T) {
			a, intents := modelBatchFixture(t)
			if fault == "later_route" {
				intents[1].Provider = "unqualified"
			} else if fault == "capacity" {
				route := a.cfg.Catalog.routes[0]
				route.InputHardLimit = 600
				catalog, err := NewCatalog([]RouteBound{route})
				if err != nil {
					t.Fatal(err)
				}
				a.cfg.Catalog = catalog
			} else {
				if _, err := a.db.Exec(`CREATE FUNCTION fail_batch_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected batch commit failure'; END $$; CREATE CONSTRAINT TRIGGER fail_batch_commit AFTER INSERT ON budget_actions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_batch_commit()`); err != nil {
					t.Fatal(err)
				}
			}
			if p, err := a.PrepareModelBatch(context.Background(), intents); err == nil || p != nil {
				t.Fatalf("uncommitted batch escaped: %#v %v", p, err)
			}
			assertPreparedBatchCounts(t, a, 0)
		})
	}
}

func TestModelBatchRetryCannotResetSendingOrExpandAnExistingPlan(t *testing.T) {
	a, intents := modelBatchFixture(t)
	ctx := context.Background()
	prepared, err := a.PrepareModelBatch(ctx, intents)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := a.ClaimPrepared(ctx, prepared[0], "batch-owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.BeginPrepared(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if p, err := a.PrepareModelBatch(ctx, intents); err == nil || p != nil {
		t.Fatalf("retry accepted a sending batch: %#v %v", p, err)
	}
	var sending, preparedCount, holds int
	if err = a.db.QueryRow(`SELECT (SELECT count(*) FROM budget_actions WHERE state='sending'),(SELECT count(*) FROM budget_actions WHERE state='prepared'),(SELECT count(*) FROM budget_holds)`).Scan(&sending, &preparedCount, &holds); err != nil {
		t.Fatal(err)
	}
	if sending != 1 || preparedCount != 1 || holds != 6 {
		t.Fatal("retry rewrote effects or holds")
	}
}

func TestModelBatchPreparationRejectsIncompleteOrMixedPlans(t *testing.T) {
	for _, fault := range []string{"missing", "duplicate", "reordered", "job", "checkpoint", "source", "validator", "preexisting_partial", "preexisting_foreign"} {
		t.Run(fault, func(t *testing.T) {
			a, intents := modelBatchFixture(t)
			want := 0
			switch fault {
			case "missing":
				intents = intents[:1]
			case "duplicate":
				intents[1] = intents[0]
			case "reordered":
				intents[0], intents[1] = intents[1], intents[0]
			case "job":
				copy := *intents[1].Job
				copy.Token = "other-owner"
				intents[1].Job = &copy
			case "checkpoint":
				intents[1].ModelCheckpoint = nil
			case "source", "validator":
				cp, err := review.DecodeModelCheckpoint(intents[1].ModelCheckpoint)
				if err != nil {
					t.Fatal(err)
				}
				if fault == "source" {
					cp.Source.Diff.Files[0].Patch = "+changed after plan"
				} else {
					cp.Validation = nil
				}
				intents[1].ModelCheckpoint, err = json.Marshal(cp)
				if err != nil {
					t.Fatal(err)
				}
			case "preexisting_partial", "preexisting_foreign":
				i := intents[0]
				if fault == "preexisting_foreign" {
					i.ActionID = "outside-plan"
				}
				if _, err := a.PrepareAction(context.Background(), i); err != nil {
					t.Fatal(err)
				}
				want = 1
			}
			if p, err := a.PrepareModelBatch(context.Background(), intents); err == nil || p != nil {
				t.Fatalf("invalid batch accepted: %#v %v", p, err)
			}
			assertPreparedBatchCounts(t, a, want)
		})
	}
}
