package accounting

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/pipeline"
	"github.com/Y4NN777/7review/agent/review"
)

type countedModelReducer struct{ calls atomic.Int32 }

func (r *countedModelReducer) ReduceModelResults(ctx context.Context, in []orchestrator.RecordedModelResult) (review.ModelReduction, error) {
	r.calls.Add(1)
	return (pipeline.ModelReducerV1{}).ReduceModelResults(ctx, in)
}

func recordedReductionFixture(t *testing.T) (*Authority, orchestrator.DispatchIntent, *review.JobClaim) {
	a, i, claim, _ := recordedReductionFixtureDSN(t)
	return a, i, claim
}

func recordedReductionFixtureDSN(t *testing.T) (*Authority, orchestrator.DispatchIntent, *review.JobClaim, string) {
	return recordedReductionFixtureResponse(t, []byte("[]"))
}

func recordedReductionFixtureResponse(t *testing.T, response []byte) (*Authority, orchestrator.DispatchIntent, *review.JobClaim, string) {
	t.Helper()
	return recordedReductionFixtureResponseWithLimits(t, response, ledgerLimits())
}

func recordedReductionFixtureResponseWithLimits(t *testing.T, response []byte, limits review.ResourceLimits) (*Authority, orchestrator.DispatchIntent, *review.JobClaim, string) {
	t.Helper()
	return recordedReductionFixtureResponseMode(t, response, limits, true)
}

func recordedReductionFixtureResponseMode(t *testing.T, response []byte, limits review.ResourceLimits, reclaim bool) (*Authority, orchestrator.DispatchIntent, *review.JobClaim, string) {
	t.Helper()
	a, i, dsn := preparedActionFixtureWithLimits(t, limits)
	ctx := context.Background()
	agg, err := a.LoadAggregate(ctx, *i.Job)
	if err != nil {
		t.Fatal(err)
	}
	agg.Source.Diff = &review.StructuredDiff{Files: []review.FileDiff{{Path: "service.go", Patch: "+change"}}}
	i.Invocation = "complete"
	i.Step = "step5"
	cpctx, err := review.WithModelCheckpoint(review.WithModelValidation(ctx, &review.ModelValidation{Version: 1, MinConfidence: 0.45}), &review.Context{Source: agg.Source}, review.ModelCursor{Phase: review.ModelReviewBatches, BatchCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	i.ModelCheckpoint = review.ModelCheckpointFromContext(cpctx)
	p, err := a.PrepareAction(ctx, i)
	if err != nil {
		t.Fatal(err)
	}
	var c orchestrator.ActionClaim
	if reclaim {
		c, err = a.ReclaimPrepared(ctx, p, "recovery", time.Minute)
	} else {
		c, err = a.ClaimPrepared(ctx, p, "live-owner", time.Minute)
	}
	if err != nil {
		t.Fatal(err)
	}
	permit, err := a.BeginPrepared(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.RecordOutcome(ctx, permit, orchestrator.DispatchOutcome{Response: response, ResponseReceived: true, ResponseDigest: hash(response), ElapsedMS: 1}); err != nil {
		t.Fatal(err)
	}
	if n, err := a.RecoverActionResults(ctx, 8); err != nil || n != 1 {
		t.Fatalf("indexing: %d %v", n, err)
	}
	return a, i, c.Job, dsn
}

func TestPostgresLocalReductionIsAtomicAndCannotGrantDispatch(t *testing.T) {
	a, i, claim := recordedReductionFixture(t)
	ctx := context.Background()
	reducer := &countedModelReducer{}
	_, err := a.db.Exec(`CREATE FUNCTION fail_model_reduction() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected reduction commit failure'; END $$; CREATE CONSTRAINT TRIGGER fail_model_reduction AFTER INSERT ON budget_model_reductions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_model_reduction()`)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := a.RecoverModelReductions(ctx, 128, reducer); err == nil || n != 0 {
		t.Fatalf("uncommitted reduction escaped: %d %v", n, err)
	}
	for _, table := range []string{"budget_model_reductions", "budget_model_reduction_inputs"} {
		var n int
		if err := a.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s escaped rollback: %d %v", table, n, err)
		}
	}
	if _, err = a.db.Exec(`DROP TRIGGER fail_model_reduction ON budget_model_reductions`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	counts := make(chan int, 2)
	errs := make(chan error, 2)
	for index := 0; index < 2; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reopened := ledgerAuthority(t, a.db, ledgerLimits())
			n, err := reopened.RecoverModelReductions(ctx, 128, reducer)
			counts <- n
			errs <- err
		}()
	}
	wg.Wait()
	close(counts)
	close(errs)
	total := 0
	for n := range counts {
		total += n
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if total != 1 || reducer.calls.Load() != 2 {
		t.Fatalf("duplicate committed reduction: total=%d reducer calls=%d", total, reducer.calls.Load())
	}
	artifact, err := a.LookupModelReduction(ctx, i.ActionID)
	if err != nil || artifact.State != review.ModelReductionValidated || artifact.Source.Report.Draft == "" || artifact.Source.Report.Final != "" || len(artifact.Inputs) != 1 {
		t.Fatalf("durable artifact lost: %#v %v", artifact, err)
	}
	if n, err := a.RecoverModelReductions(ctx, 128, reducer); err != nil || n != 0 || reducer.calls.Load() != 2 {
		t.Fatalf("replay repeated reduction: %d %v", n, err)
	}
	other := i
	other.ActionID = "unrestricted-next-action"
	other.Job = claim
	if _, err = a.PrepareAction(ctx, other); err == nil {
		t.Fatal("local reduction cleared recovery dispatch restriction")
	}
	var actions, holds, settled int
	_ = a.db.QueryRow(`SELECT count(*),count(*) FILTER(WHERE settled) FROM budget_actions`).Scan(&actions, &settled)
	_ = a.db.QueryRow(`SELECT count(*) FROM budget_holds`).Scan(&holds)
	if actions != 1 || holds != 3 || settled != 0 {
		t.Fatalf("local reduction changed spending: %d %d %d", actions, holds, settled)
	}
}

func TestPostgresLocalReductionRejectsSwappedOrUnqualifiedJournal(t *testing.T) {
	for _, fault := range []string{"response", "action outcome", "checkpoint", "namespace"} {
		t.Run(fault, func(t *testing.T) {
			a, i, _ := recordedReductionFixture(t)
			ctx := context.Background()
			var err error
			switch fault {
			case "response":
				_, err = a.db.Exec(`UPDATE budget_action_results SET response_digest='changed'`)
			case "action outcome":
				_, err = a.db.Exec(`UPDATE budget_action_results SET outcome=convert_to('{}','UTF8')`)
			case "checkpoint":
				_, err = a.db.Exec(`UPDATE budget_actions SET intent_digest='changed'`)
			case "namespace":
				a.cfg.ProjectID = "other"
			}
			if err != nil {
				t.Fatal(err)
			}
			n, err := a.RecoverModelReductions(ctx, 128, &countedModelReducer{})
			if fault == "namespace" {
				if err != nil || n != 0 {
					t.Fatalf("namespace scan: %d %v", n, err)
				}
			} else if err == nil || n != 0 {
				t.Fatalf("bad journal reduced: %d %v", n, err)
			}
			if _, err = a.LookupModelReduction(ctx, i.ActionID); err == nil {
				t.Fatal("invalid reduction became visible")
			}
		})
	}
}

type panicModelReducer struct{}

func (panicModelReducer) ReduceModelResults(context.Context, []orchestrator.RecordedModelResult) (review.ModelReduction, error) {
	panic("local reducer died")
}

func TestPostgresReducerPanicRollsBackWithoutProviderRetry(t *testing.T) {
	a, _, _ := recordedReductionFixture(t)
	if n, err := a.RecoverModelReductions(context.Background(), 128, panicModelReducer{}); err == nil || n != 0 {
		t.Fatalf("panic escaped as success: %d %v", n, err)
	}
	if n, err := a.RecoverModelReductions(context.Background(), 128, &countedModelReducer{}); err != nil || n != 1 {
		t.Fatalf("local retry failed: %d %v", n, err)
	}
	if _, err := a.LookupModelReduction(context.Background(), "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing artifact: %v", err)
	}
}

type exitingModelReducer struct{}

func (exitingModelReducer) ReduceModelResults(ctx context.Context, inputs []orchestrator.RecordedModelResult) (review.ModelReduction, error) {
	artifact, err := (pipeline.ModelReducerV1{}).ReduceModelResults(ctx, inputs)
	if err == nil {
		os.Exit(71)
	}
	return artifact, err
}

func TestModelReductionCrashChild(t *testing.T) {
	stage := os.Getenv("SEVENREVIEW_MODEL_REDUCTION_CRASH")
	if stage == "" {
		t.Skip("subprocess helper")
	}
	db, err := sql.Open("pgx", os.Getenv("SEVENREVIEW_MODEL_REDUCTION_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	a := ledgerAuthority(t, db, ledgerLimits())
	var reducer orchestrator.ModelResultReducer = &countedModelReducer{}
	if stage == "before_commit" {
		reducer = exitingModelReducer{}
	}
	if n, err := a.RecoverModelReductions(context.Background(), 128, reducer); err != nil || n != 1 {
		t.Fatalf("reduction child: n=%d err=%v", n, err)
	}
	os.Exit(72)
}

func TestPostgresModelReductionProcessDeathBeforeAndAfterCommit(t *testing.T) {
	for _, stage := range []string{"before_commit", "after_commit"} {
		t.Run(stage, func(t *testing.T) {
			a, i, _, dsn := recordedReductionFixtureDSN(t)
			child := exec.Command(os.Args[0], "-test.run=^TestModelReductionCrashChild$")
			child.Env = append(os.Environ(), "SEVENREVIEW_MODEL_REDUCTION_CRASH="+stage, "SEVENREVIEW_MODEL_REDUCTION_DSN="+dsn)
			err := child.Run()
			wantExit := 71
			if stage == "after_commit" {
				wantExit = 72
			}
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != wantExit {
				t.Fatalf("child did not reach boundary: %v", err)
			}
			reopened := ledgerAuthority(t, a.db, ledgerLimits())
			reducer := &countedModelReducer{}
			n, err := reopened.RecoverModelReductions(context.Background(), 128, reducer)
			want := 1
			if stage == "after_commit" {
				want = 0
			}
			if err != nil || n != want || reducer.calls.Load() != int32(want) {
				t.Fatalf("process restart reran effects or lost reduction: n=%d local_calls=%d err=%v", n, reducer.calls.Load(), err)
			}
			if _, err = reopened.LookupModelReduction(context.Background(), i.ActionID); err != nil {
				t.Fatal(err)
			}
			var actions, holds, links int
			_ = a.db.QueryRow(`SELECT count(*) FROM budget_actions`).Scan(&actions)
			_ = a.db.QueryRow(`SELECT count(*) FROM budget_holds`).Scan(&holds)
			_ = a.db.QueryRow(`SELECT count(*) FROM budget_model_reduction_inputs`).Scan(&links)
			if actions != 1 || holds != 3 || links != 1 {
				t.Fatalf("process loss reset state: %d %d %d", actions, holds, links)
			}
		})
	}
}

func TestPostgresLocalReductionWaitsForEveryBatchAndJoinsByIndex(t *testing.T) {
	a, i, _ := preparedActionFixture(t)
	ctx := context.Background()
	agg, err := a.LoadAggregate(ctx, *i.Job)
	if err != nil {
		t.Fatal(err)
	}
	agg.Source.Diff = &review.StructuredDiff{Files: []review.FileDiff{{Path: "service.go", Patch: "+change"}}}
	cpctx, err := review.WithModelCheckpoint(review.WithModelValidation(ctx, &review.ModelValidation{Version: 1, MinConfidence: 0.45}), &review.Context{Source: agg.Source}, review.ModelCursor{Phase: review.ModelReviewBatches, BatchCount: 2})
	if err != nil {
		t.Fatal(err)
	}
	reducer := &countedModelReducer{}
	for pass, index := range []int{1, 0} {
		intent := i
		intent.Invocation = "complete"
		intent.ActionID = fmt.Sprintf("batch-%d", index)
		intent.Step = fmt.Sprintf("step5_batch%d", index+1)
		batchCtx, err := review.WithModelBatch(cpctx, index, 2)
		if err != nil {
			t.Fatal(err)
		}
		intent.ModelCheckpoint = review.ModelCheckpointFromContext(batchCtx)
		p, err := a.PrepareAction(ctx, intent)
		if err != nil {
			t.Fatal(err)
		}
		claim, err := a.ClaimPrepared(ctx, p, "batch-owner", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		permit, err := a.BeginPrepared(ctx, claim)
		if err != nil {
			t.Fatal(err)
		}
		response := []byte("[]")
		if err = a.RecordOutcome(ctx, permit, orchestrator.DispatchOutcome{Response: response, ResponseReceived: true, ResponseDigest: hash(response), ElapsedMS: 1}); err != nil {
			t.Fatal(err)
		}
		if _, err = a.RecoverActionResults(ctx, 8); err != nil {
			t.Fatal(err)
		}
		n, err := a.RecoverModelReductions(ctx, 128, reducer)
		if err != nil || n != pass || reducer.calls.Load() != int32(pass) {
			t.Fatalf("batch group reduced early or out of order: n=%d calls=%d err=%v", n, reducer.calls.Load(), err)
		}
	}
	artifact, err := a.LookupModelReduction(ctx, "batch-1")
	if err != nil || len(artifact.Inputs) != 2 || artifact.Inputs[0].ActionID != "batch-0" || artifact.Inputs[1].ActionID != "batch-1" {
		t.Fatalf("arrival-dependent stored reduction: %#v %v", artifact, err)
	}
}

type faultyModelReducer string

func (fault faultyModelReducer) ReduceModelResults(ctx context.Context, inputs []orchestrator.RecordedModelResult) (review.ModelReduction, error) {
	if fault == "mutable inputs" {
		inputs[0].Intent.ActionID = "forged"
	}
	artifact, err := (pipeline.ModelReducerV1{}).ReduceModelResults(ctx, inputs)
	if err != nil {
		return artifact, err
	}
	switch fault {
	case "input receipt":
		artifact.Inputs[0].RequestDigest = "forged"
	case "evidence":
		artifact.Source.Evidence = append(artifact.Source.Evidence, review.EvidenceItem{Source: "forged-contract"})
	case "validator":
		artifact.Validation.MinConfidence = 0.01
	case "next phase":
		artifact.State = review.ModelReductionNeedsTools
		artifact.Source.Report.Draft = ""
		artifact.NextCursor = &review.ModelCursor{Phase: review.ModelReviewBatches, BatchCount: 1}
	}
	return artifact, nil
}

func TestPostgresReductionAdapterCannotRewriteReceiptsEvidenceOrAuthority(t *testing.T) {
	for _, fault := range []faultyModelReducer{"input receipt", "evidence", "validator", "mutable inputs", "next phase"} {
		t.Run(string(fault), func(t *testing.T) {
			a, _, _ := recordedReductionFixture(t)
			if n, err := a.RecoverModelReductions(context.Background(), 128, fault); err == nil || n != 0 {
				t.Fatalf("invalid adapter output committed: %d %v", n, err)
			}
			var count int
			if err := a.db.QueryRow(`SELECT count(*) FROM budget_model_reductions`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("invalid artifact visible: %d %v", count, err)
			}
			if n, err := a.RecoverModelReductions(context.Background(), 128, &countedModelReducer{}); err != nil || n != 1 {
				t.Fatalf("bad adapter changed immutable inputs: %d %v", n, err)
			}
		})
	}
}
