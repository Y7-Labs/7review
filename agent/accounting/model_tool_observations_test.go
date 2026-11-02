package accounting

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/pipeline"
	"github.com/Y4NN777/7review/agent/review"
)

type countedToolObserver struct{ calls atomic.Int32 }

func (o *countedToolObserver) ObserveModelTools(ctx context.Context, parent review.ModelReduction) (review.ModelToolObservations, error) {
	o.calls.Add(1)
	return (pipeline.ModelToolObserverV1{}).ObserveModelTools(ctx, parent)
}

func toolParentFixture(t *testing.T) (*Authority, orchestrator.DispatchIntent, *review.JobClaim) {
	t.Helper()
	return toolParentFixtureWithLimits(t, ledgerLimits())
}

func toolParentFixtureWithLimits(t *testing.T, limits review.ResourceLimits) (*Authority, orchestrator.DispatchIntent, *review.JobClaim) {
	t.Helper()
	a, i, claim, _ := toolParentFixtureDSN(t, limits)
	return a, i, claim
}

func toolParentFixtureDSN(t *testing.T, limits review.ResourceLimits) (*Authority, orchestrator.DispatchIntent, *review.JobClaim, string) {
	t.Helper()
	a, i, claim, dsn := recordedReductionFixtureResponseWithLimits(t, []byte(`{"tool_requests":[{"name":"get_changed_files"}]}`), limits)
	if n, err := a.RecoverModelReductions(context.Background(), 8, pipeline.ModelReducerV1{}); err != nil || n != 1 {
		t.Fatalf("tool parent: %d %v", n, err)
	}
	return a, i, claim, dsn
}

func TestPostgresToolObservationCommitIsAtomicIdempotentAndConfined(t *testing.T) {
	a, i, old := toolParentFixture(t)
	ctx := context.Background()
	observer := &countedToolObserver{}
	if _, err := a.db.Exec(`CREATE FUNCTION fail_tool_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected observation commit failure'; END $$; CREATE CONSTRAINT TRIGGER fail_tool_commit AFTER INSERT ON budget_model_tool_observations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_tool_commit()`); err != nil {
		t.Fatal(err)
	}
	if artifact, err := a.CommitModelToolObservations(ctx, i.ActionID, observer); err == nil || artifact != nil {
		t.Fatalf("uncommitted observations escaped: %#v %v", artifact, err)
	}
	var token string
	var observations, links int
	if err := a.db.QueryRow(`SELECT i.claim_token,(SELECT count(*) FROM budget_model_tool_observations),(SELECT count(*) FROM budget_tool_observation_scopes) FROM budget_inbox i`).Scan(&token, &observations, &links); err != nil || token != old.Token || observations != 0 || links != 0 {
		t.Fatalf("rollback changed authority or charges: %s %d %d %v", token, observations, links, err)
	}
	if _, err := a.db.Exec(`DROP TRIGGER fail_tool_commit ON budget_model_tool_observations`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reopened := ledgerAuthority(t, a.db, ledgerLimits())
			_, err := reopened.CommitModelToolObservations(ctx, i.ActionID, observer)
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
	if observer.calls.Load() != 2 { // rolled-back rendering plus one committed rendering
		t.Fatalf("duplicate worker rerendered committed data: %d", observer.calls.Load())
	}
	var cursor, pointer string
	var actions, holds, settled int
	if err := a.db.QueryRow(`SELECT i.claim_token,j.recovery_action_id,j.tool_observation_id,(SELECT count(*) FROM budget_model_tool_observations),(SELECT count(*) FROM budget_tool_observation_scopes),(SELECT count(*) FROM budget_actions),(SELECT count(*) FROM budget_holds),(SELECT count(*) FROM budget_actions WHERE settled) FROM budget_inbox i JOIN budget_jobs j USING(run_id)`).Scan(&token, &cursor, &pointer, &observations, &links, &actions, &holds, &settled); err != nil {
		t.Fatal(err)
	}
	if token == old.Token || cursor != i.ActionID || pointer == "" || observations != 1 || links != 3 || actions != 1 || holds != 3 || settled != 0 {
		t.Fatalf("observation commit widened execution or released holds: %s %s %s obs=%d links=%d actions=%d holds=%d settled=%d", token, cursor, pointer, observations, links, actions, holds, settled)
	}
	for _, claim := range []review.JobClaim{*old, {RunID: old.RunID, Token: token}} {
		if err := a.FinishInbox(ctx, claim.RunID, claim.Token, true); err == nil {
			t.Fatal("observation stage granted whole-job completion")
		}
	}
	artifact, err := a.LookupModelToolObservations(ctx, i.ActionID)
	if err != nil || len(artifact.Observations) != 1 || artifact.Observations[0].Status != "denied" {
		t.Fatalf("restart lost denied evaluation: %#v %v", artifact, err)
	}
	if _, err := a.db.Exec(`UPDATE budget_attempts SET active=false; UPDATE budget_inbox SET state='uncertain',claim_token='later-epoch',deadline=clock_timestamp()`); err != nil {
		t.Fatal(err)
	}
	replay, err := a.CommitModelToolObservations(ctx, i.ActionID, observer)
	if err != nil || !sameModelJSON(replay, artifact) || observer.calls.Load() != 2 {
		t.Fatalf("historical replay reactivated or recharged work: %#v %v", replay, err)
	}
	if err := a.db.QueryRow(`SELECT claim_token FROM budget_inbox`).Scan(&token); err != nil || token != "later-epoch" {
		t.Fatalf("read replay changed epoch: %s %v", token, err)
	}
	a.cfg.Limits.Attempt.ToolCalls = 0
	if historical, err := a.LookupModelToolObservations(ctx, i.ActionID); err != nil || !sameModelJSON(historical, artifact) {
		t.Fatalf("later operator configuration rewrote historical evidence: %#v %v", historical, err)
	}
}

func TestPostgresToolObservationCommitRejectsStaleOrUnprovenParents(t *testing.T) {
	for _, fault := range []string{"epoch", "attempt", "head", "deadline", "foreign_namespace", "sending_fence", "parent_digest", "unlinked_sibling", "missing_hold", "operator_change"} {
		t.Run(fault, func(t *testing.T) {
			a, i, _ := toolParentFixture(t)
			query := ""
			switch fault {
			case "epoch":
				query = `UPDATE budget_inbox SET claim_token='unrelated-owner'`
			case "attempt":
				query = `UPDATE budget_attempts SET active=false`
			case "head":
				query = `UPDATE budget_heads SET current_attempt_id=NULL`
			case "deadline":
				query = `UPDATE budget_inbox SET deadline=clock_timestamp()`
			case "foreign_namespace":
				a.cfg.ProjectID = "other-project"
			case "sending_fence":
				query = `UPDATE budget_action_leases SET sending_fence=NULL`
			case "parent_digest":
				query = `UPDATE budget_model_reductions SET artifact_digest='changed'`
			case "unlinked_sibling":
				query = `INSERT INTO budget_actions(id,attempt_id,request_digest,state,upper_amount,quote,request,intent_digest,fenced) SELECT 'unlinked-sibling',attempt_id,request_digest,state,upper_amount,quote,request,intent_digest,fenced FROM budget_actions`
			case "missing_hold":
				query = `DELETE FROM budget_holds WHERE scope_id LIKE 'project:%'`
			case "operator_change":
				a.cfg.Limits.Attempt.ToolCalls--
			}
			if query != "" {
				if _, err := a.db.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			observer := &countedToolObserver{}
			if artifact, err := a.CommitModelToolObservations(context.Background(), i.ActionID, observer); err == nil || artifact != nil || observer.calls.Load() != 0 {
				t.Fatalf("%s rendered unowned/unproven work: %#v calls=%d %v", fault, artifact, observer.calls.Load(), err)
			}
		})
	}
}

func TestPostgresToolObservationChargesParticipateInEveryScopeCapacity(t *testing.T) {
	a, i, _ := toolParentFixture(t)
	ctx := context.Background()
	artifact, err := a.CommitModelToolObservations(ctx, i.ActionID, pipeline.ModelToolObserverV1{})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := a.LookupModelReduction(ctx, i.ActionID)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := artifact.SnapshotUsage(parent)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := a.binding(i)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range a.scopeRefs(i, binding) {
		tx, err := a.begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var recordTimeJSON []byte
		if err = tx.QueryRow(`SELECT receipt FROM budget_model_tool_observations`).Scan(&recordTimeJSON); err != nil {
			t.Fatal(err)
		}
		var record toolObservationRecord
		if err = json.Unmarshal(recordTimeJSON, &record); err != nil {
			t.Fatal(err)
		}
		if ref.kind != "attempt" {
			ref.origin, err = periodID(ref.limit.Period, record.AccountedAt)
			if err != nil {
				t.Fatal(err)
			}
		}
		ref.limit.ToolCalls = usage.ToolCalls
		ref.limit.ObservationBytes = usage.ObservationBytes
		if err = checkCapacity(ctx, tx, ref, review.ResourceLimitScope{}); err != nil {
			t.Fatal(err)
		}
		if err = checkCapacity(ctx, tx, ref, review.ResourceLimitScope{ToolCalls: 1}); err == nil {
			t.Fatalf("%s ignored tool charges", ref.kind)
		}
		if err = checkCapacity(ctx, tx, ref, review.ResourceLimitScope{ObservationBytes: 1}); err == nil {
			t.Fatalf("%s ignored observation bytes", ref.kind)
		}
		tx.Rollback()
	}
}

type failingToolObserver struct{ panic bool }

func (o failingToolObserver) ObserveModelTools(context.Context, review.ModelReduction) (review.ModelToolObservations, error) {
	if o.panic {
		panic("interrupted adapter")
	}
	return review.ModelToolObservations{}, errors.New("interrupted adapter")
}

func TestPostgresToolObservationInterruptedRendererCannotCommit(t *testing.T) {
	for _, panics := range []bool{false, true} {
		a, i, old := toolParentFixture(t)
		if artifact, err := a.CommitModelToolObservations(context.Background(), i.ActionID, failingToolObserver{panic: panics}); err == nil || artifact != nil {
			t.Fatalf("interruption escaped: %#v %v", artifact, err)
		}
		var token string
		var n int
		if err := a.db.QueryRow(`SELECT claim_token,(SELECT count(*) FROM budget_model_tool_observations) FROM budget_inbox`).Scan(&token, &n); err != nil || token != old.Token || n != 0 {
			t.Fatalf("interruption changed authority/accounting: %s %d %v", token, n, err)
		}
	}
}

func TestPostgresToolObservationBudgetDenialCommitsNeitherChargeNorEpoch(t *testing.T) {
	for _, kind := range []string{"attempt", "change", "project"} {
		for _, dimension := range []string{"tool_calls", "observation_bytes"} {
			t.Run(kind+"/"+dimension, func(t *testing.T) {
				limits := ledgerLimits()
				scopes := map[string]*review.ResourceLimitScope{"attempt": &limits.Attempt, "change": &limits.Change, "project": &limits.Project}
				wantRenders := int32(0)
				if dimension == "tool_calls" {
					scopes[kind].ToolCalls = 0
				} else {
					scopes[kind].ObservationBytes = 1
					wantRenders = 1
				}
				a, i, old := toolParentFixtureWithLimits(t, limits)
				observer := &countedToolObserver{}
				if artifact, err := a.CommitModelToolObservations(context.Background(), i.ActionID, observer); err == nil || artifact != nil || observer.calls.Load() != wantRenders {
					t.Fatalf("budget bypass: %#v calls=%d want=%d %v", artifact, observer.calls.Load(), wantRenders, err)
				}
				var token string
				var records, holds int
				if err := a.db.QueryRow(`SELECT claim_token,(SELECT count(*) FROM budget_model_tool_observations),(SELECT count(*) FROM budget_holds) FROM budget_inbox`).Scan(&token, &records, &holds); err != nil || token != old.Token || records != 0 || holds != 3 {
					t.Fatalf("budget denial changed durable state: %s %d %d %v", token, records, holds, err)
				}
			})
		}
	}
}

func TestPostgresModelAdmissionCountsSharedSnapshotCharges(t *testing.T) {
	for _, kind := range []string{"change", "project"} {
		t.Run(kind, func(t *testing.T) {
			a, i, _ := toolParentFixture(t)
			ctx := context.Background()
			if _, err := a.CommitModelToolObservations(ctx, i.ActionID, pipeline.ModelToolObserverV1{}); err != nil {
				t.Fatal(err)
			}
			next := ledgerIntent(t, a.db, "later-attempt", i.Attempt.Change.ChangeNumber, ledgerLimits())
			policy := ledgerLimits()
			if kind == "change" {
				policy.Change.ObservationBytes = 0
			} else {
				policy.Project.ObservationBytes = 0
			}
			next.PolicyLimits = &policy
			if err := a.RegisterAttempt(ctx, next); err != nil {
				t.Fatal(err)
			}
			if p, err := a.PrepareAction(ctx, next); err == nil || p.ID != "" {
				t.Fatalf("model admission ignored prior %s snapshot charge: %#v %v", kind, p, err)
			}
			var n int
			if err := a.db.QueryRow(`SELECT count(*) FROM budget_actions`).Scan(&n); err != nil || n != 1 {
				t.Fatalf("denied reservation escaped: %d %v", n, err)
			}
		})
	}
}

func TestPostgresToolObservationCorruptionCannotEraseCapacity(t *testing.T) {
	for _, fault := range []string{"receipt_digest", "missing_link", "wrong_origin", "rehash_origin", "amount"} {
		t.Run(fault, func(t *testing.T) {
			a, i, _ := toolParentFixture(t)
			ctx := context.Background()
			if _, err := a.CommitModelToolObservations(ctx, i.ActionID, pipeline.ModelToolObserverV1{}); err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "receipt_digest":
				if _, err := a.db.Exec(`UPDATE budget_model_tool_observations SET receipt_digest='changed'`); err != nil {
					t.Fatal(err)
				}
			case "missing_link":
				if _, err := a.db.Exec(`DELETE FROM budget_tool_observation_scopes WHERE scope_id LIKE 'project:%'`); err != nil {
					t.Fatal(err)
				}
			case "wrong_origin":
				if _, err := a.db.Exec(`UPDATE budget_tool_observation_scopes SET origin_period='other-period' WHERE scope_id LIKE 'project:%'`); err != nil {
					t.Fatal(err)
				}
			case "amount", "rehash_origin":
				var raw []byte
				if err := a.db.QueryRow(`SELECT receipt FROM budget_model_tool_observations`).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var record toolObservationRecord
				if err := json.Unmarshal(raw, &record); err != nil {
					t.Fatal(err)
				}
				if fault == "amount" {
					record.Amount.ObservationBytes = 0
				} else {
					record.Scopes[2].Origin = "0"
					if _, err := a.db.Exec(`UPDATE budget_tool_observation_scopes SET origin_period='0' WHERE scope_id LIKE 'project:%'`); err != nil {
						t.Fatal(err)
					}
				}
				raw, _ = json.Marshal(record)
				if _, err := a.db.Exec(`UPDATE budget_model_tool_observations SET receipt=$1,receipt_digest=$2`, raw, hash(raw)); err != nil {
					t.Fatal(err)
				}
			}
			if artifact, err := a.LookupModelToolObservations(ctx, i.ActionID); err == nil || artifact != nil {
				t.Fatalf("corrupt receipt returned as data: %#v %v", artifact, err)
			}
			binding, err := a.binding(i)
			if err != nil {
				t.Fatal(err)
			}
			ref := a.scopeRefs(i, binding)[2]
			tx, err := a.begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err := checkCapacity(ctx, tx, ref, review.ResourceLimitScope{}); err == nil {
				t.Fatal("corrupt/missing charge silently became free capacity")
			}
		})
	}
}

type exitToolObserver struct{}

func (exitToolObserver) ObserveModelTools(ctx context.Context, parent review.ModelReduction) (review.ModelToolObservations, error) {
	_, err := (pipeline.ModelToolObserverV1{}).ObserveModelTools(ctx, parent)
	if err != nil {
		return review.ModelToolObservations{}, err
	}
	os.Exit(71)
	return review.ModelToolObservations{}, errors.New("unreachable")
}

func TestToolObservationCrashChild(t *testing.T) {
	stage := os.Getenv("SEVENREVIEW_TOOL_CRASH_STAGE")
	if stage == "" {
		t.Skip("subprocess helper")
	}
	db, err := sql.Open("pgx", os.Getenv("SEVENREVIEW_TOOL_CRASH_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := ledgerAuthority(t, db, ledgerLimits())
	var observer orchestrator.ModelToolObserver = pipeline.ModelToolObserverV1{}
	if stage == "render" {
		observer = exitToolObserver{}
	}
	if _, err := a.CommitModelToolObservations(context.Background(), os.Getenv("SEVENREVIEW_TOOL_CRASH_ACTION"), observer); err != nil {
		t.Fatal(err)
	}
	os.Exit(71)
}

func TestPostgresToolObservationProcessDeathDoesNotDoubleChargeOrReplayModel(t *testing.T) {
	for _, stage := range []string{"render", "committed"} {
		t.Run(stage, func(t *testing.T) {
			a, i, old, dsn := toolParentFixtureDSN(t, ledgerLimits())
			child := exec.Command(os.Args[0], "-test.run=^TestToolObservationCrashChild$")
			child.Env = append(os.Environ(), "SEVENREVIEW_TOOL_CRASH_STAGE="+stage, "SEVENREVIEW_TOOL_CRASH_DSN="+dsn, "SEVENREVIEW_TOOL_CRASH_ACTION="+i.ActionID)
			if output, err := child.CombinedOutput(); err == nil {
				t.Fatal("crash child did not exit")
			} else if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 71 {
				t.Fatalf("crash boundary: %v %s", err, output)
			}
			observer := &countedToolObserver{}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			reopened := ledgerAuthority(t, a.db, ledgerLimits())
			if _, err := reopened.CommitModelToolObservations(ctx, i.ActionID, observer); err != nil {
				t.Fatal(err)
			}
			want := int32(0)
			if stage == "render" {
				want = 1
			}
			if observer.calls.Load() != want {
				t.Fatalf("restart rerendered committed observations: %d want %d", observer.calls.Load(), want)
			}
			var token, state string
			var records, links, actions, holds int
			if err := a.db.QueryRow(`SELECT i.claim_token,a.state,(SELECT count(*) FROM budget_model_tool_observations),(SELECT count(*) FROM budget_tool_observation_scopes),(SELECT count(*) FROM budget_actions),(SELECT count(*) FROM budget_holds) FROM budget_inbox i CROSS JOIN budget_actions a`).Scan(&token, &state, &records, &links, &actions, &holds); err != nil {
				t.Fatal(err)
			}
			if token == old.Token || state != "result_recorded" || records != 1 || links != 3 || actions != 1 || holds != 3 {
				t.Fatalf("restart replayed or reset state: token=%s state=%s records=%d links=%d actions=%d holds=%d", token, state, records, links, actions, holds)
			}
		})
	}
}

func TestPostgresPreparedClaimAndBeginRecheckSharedSnapshotCharges(t *testing.T) {
	for _, kind := range []string{"change", "project"} {
		for _, stage := range []string{"claim", "begin"} {
			t.Run(kind+"/"+stage, func(t *testing.T) {
				a, i, _ := toolParentFixture(t)
				ctx := context.Background()
				next := ledgerIntent(t, a.db, "waiting-action", i.Attempt.Change.ChangeNumber, ledgerLimits())
				policy := ledgerLimits()
				if kind == "change" {
					policy.Change.ObservationBytes = 0
				} else {
					policy.Project.ObservationBytes = 0
				}
				next.PolicyLimits = &policy
				if err := a.RegisterAttempt(ctx, next); err != nil {
					t.Fatal(err)
				}
				prepared, err := a.PrepareAction(ctx, next)
				if err != nil {
					t.Fatal(err)
				}
				var claim orchestrator.ActionClaim
				if stage == "begin" {
					claim, err = a.ClaimPrepared(ctx, prepared, "waiting-worker", time.Minute)
					if err != nil {
						t.Fatal(err)
					}
				}
				if _, err := a.CommitModelToolObservations(ctx, i.ActionID, pipeline.ModelToolObserverV1{}); err != nil {
					t.Fatal(err)
				}
				if stage == "claim" {
					if c, err := a.ClaimPrepared(ctx, prepared, "waiting-worker", time.Minute); err == nil || c.ID != "" {
						t.Fatalf("claim skipped current %s charges: %#v %v", kind, c, err)
					}
				} else if p, err := a.BeginPrepared(ctx, claim); err == nil || p.ID != "" {
					t.Fatalf("sending skipped current %s charges: %#v %v", kind, p, err)
				}
				var state string
				if err := a.db.QueryRow(`SELECT state FROM budget_actions WHERE id=$1`, prepared.ID).Scan(&state); err != nil || state != "prepared" {
					t.Fatalf("denied dispatch escaped: %s %v", state, err)
				}
			})
		}
	}
}

func TestPostgresLiveToolObservationCommitRevokesOriginalFrame(t *testing.T) {
	a, i, old, _ := recordedReductionFixtureResponseMode(t, []byte(`{"tool_requests":[{"name":"get_changed_files"}]}`), ledgerLimits(), false)
	ctx := context.Background()
	if _, err := a.RecoverModelReductions(ctx, 8, pipeline.ModelReducerV1{}); err != nil {
		t.Fatal(err)
	}
	var unconfined bool
	if err := a.db.QueryRow(`SELECT recovery_action_id IS NULL FROM budget_jobs`).Scan(&unconfined); err != nil || !unconfined || old.Token != i.Job.Token {
		t.Fatalf("fixture was not the original unconfined frame: %v %v", unconfined, err)
	}
	if _, err := a.CommitModelToolObservations(ctx, i.ActionID, pipeline.ModelToolObserverV1{}); err != nil {
		t.Fatal(err)
	}
	var token string
	if err := a.db.QueryRow(`SELECT claim_token FROM budget_inbox`).Scan(&token); err != nil {
		t.Fatal(err)
	}
	for _, success := range []bool{false, true} {
		if err := a.FinishInbox(ctx, old.RunID, old.Token, success); err == nil {
			t.Fatal("old live frame finished/failed an observed-tools job")
		}
	}
	for _, epoch := range []string{old.Token, token} {
		next := i
		next.ActionID += "-unlinked-followup"
		next.Job = &review.JobClaim{RunID: old.RunID, Token: epoch}
		if p, err := a.PrepareAction(ctx, next); err == nil || p.ID != "" {
			t.Fatalf("observation commit left an unlinked dispatch grant: %#v %v", p, err)
		}
	}
}

type toolObserverFunc func(context.Context, review.ModelReduction) (review.ModelToolObservations, error)

func (f toolObserverFunc) ObserveModelTools(ctx context.Context, parent review.ModelReduction) (review.ModelToolObservations, error) {
	return f(ctx, parent)
}

func TestPostgresToolObservationPeriodIsAssignedAfterRendering(t *testing.T) {
	limits := ledgerLimits()
	limits.Change.Period.DurationMS = 1000
	limits.Project.Period.DurationMS = 1000
	a, i, _ := toolParentFixtureWithLimits(t, limits)
	ctx := context.Background()
	var before time.Time
	observer := toolObserverFunc(func(ctx context.Context, parent review.ModelReduction) (review.ModelToolObservations, error) {
		result, err := (pipeline.ModelToolObserverV1{}).ObserveModelTools(ctx, parent)
		if err != nil {
			return result, err
		}
		if err := a.db.QueryRow(`SELECT clock_timestamp()`).Scan(&before); err != nil {
			return review.ModelToolObservations{}, err
		}
		_, err = a.db.Exec(`SELECT pg_sleep(1.01)`)
		return result, err
	})
	if _, err := a.CommitModelToolObservations(ctx, i.ActionID, observer); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := a.db.QueryRow(`SELECT receipt FROM budget_model_tool_observations`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var record toolObservationRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	for n, period := range []*review.FixedBudgetPeriod{limits.Change.Period, limits.Project.Period} {
		oldPeriod, _ := periodID(period, before)
		newPeriod, _ := periodID(period, record.AccountedAt)
		if oldPeriod == newPeriod || record.Scopes[n+1].Origin != newPeriod {
			t.Fatal("snapshot charge retained a pre-render renewal period")
		}
	}
	binding, err := a.binding(i)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range a.scopeRefs(i, binding)[1:] {
		ref.origin = "future-period"
		ref.limit.ToolCalls, ref.limit.ObservationBytes = 0, 0
		tx, err := a.begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = checkCapacity(ctx, tx, ref, review.ResourceLimitScope{}); err != nil {
			tx.Rollback()
			t.Fatalf("known local charge incorrectly crossed renewal: %v", err)
		}
		tx.Rollback()
	}
	if _, err := a.LookupModelToolObservations(ctx, i.ActionID); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresCancelledToolObservationExposesNoCharge(t *testing.T) {
	a, i, old := toolParentFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observer := toolObserverFunc(func(ctx context.Context, parent review.ModelReduction) (review.ModelToolObservations, error) {
		result, err := (pipeline.ModelToolObserverV1{}).ObserveModelTools(ctx, parent)
		cancel()
		return result, err
	})
	if artifact, err := a.CommitModelToolObservations(ctx, i.ActionID, observer); !errors.Is(err, context.Canceled) || artifact != nil {
		t.Fatalf("cancelled observation exposed acceptance: %#v %v", artifact, err)
	}
	var token string
	var records int
	if err := a.db.QueryRow(`SELECT claim_token,(SELECT count(*) FROM budget_model_tool_observations) FROM budget_inbox`).Scan(&token, &records); err != nil || token != old.Token || records != 0 {
		t.Fatalf("cancellation changed durable state: %s %d %v", token, records, err)
	}
}

func toolParentForChange(t *testing.T, a *Authority, change string) orchestrator.DispatchIntent {
	t.Helper()
	return toolParentForChangeWithPolicy(t, a, change, nil)
}

func toolParentForChangeWithPolicy(t *testing.T, a *Authority, change string, policyLimits *review.ResourceLimits) orchestrator.DispatchIntent {
	t.Helper()
	ctx := context.Background()
	req, admission, source := preparedAggregate(t, a)
	req.ChangeID, req.DeliveryID = change, "tool-delivery-"+change
	admission.Request, admission.DeliveryID = req, req.DeliveryID
	admission.Change.ChangeNumber = change
	limits := a.cfg.Limits.Clone()
	policy := limits.Clone()
	if policyLimits != nil {
		policy = policyLimits.Clone()
	}
	admission.Policy.Limits, admission.Policy.OperatorCeilings = &policy, &limits
	scm := *source.SCM
	scm.ChangeID = change
	_, attestation, err := review.AttestSCMRequest(req, &scm, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	admission.Attestation = attestation
	admission.Execution.ComparisonTree = attestation.ComparisonTree
	source.Request, source.SCM, source.Attestation, source.Policy = req, &scm, &attestation, admission.Policy
	if _, err := a.AcceptAggregate(ctx, req, admission, source, nil); err != nil {
		t.Fatal(err)
	}
	job, err := a.ClaimInbox(ctx)
	if err != nil || job == nil {
		t.Fatalf("claim change %s: %#v %v", change, job, err)
	}
	claim := &review.JobClaim{RunID: job.RunID, Token: job.ClaimToken}
	aggregate, err := a.LoadAggregate(ctx, *claim)
	if err != nil {
		t.Fatal(err)
	}
	aggregate.Source.Diff = &review.StructuredDiff{Files: []review.FileDiff{{Path: "service.go", Patch: "+change"}}}
	i := dispatchForReceipt(ledgerIntent(t, a.db, "tool-parent-"+change, change, limits), aggregate.Receipt)
	i.Job, i.Invocation, i.Step = claim, "complete", "step5"
	routing := &review.ModelRouting{Version: 1, Roles: map[string]review.ModelRouteChain{"reasoner": {Routes: []review.ModelRoute{{Provider: i.Provider, Model: i.Model}}, MaxTokens: 20}}}
	cpctx, err := review.WithModelCheckpoint(review.WithModelRouting(review.WithModelValidation(ctx, &review.ModelValidation{Version: 1, MinConfidence: 0.45}), routing), &review.Context{Source: aggregate.Source}, review.ModelCursor{Phase: review.ModelReviewBatches, BatchCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	i.ModelCheckpoint = review.ModelCheckpointFromContext(cpctx)
	prepared, err := a.PrepareAction(ctx, i)
	if err != nil {
		t.Fatal(err)
	}
	owned, err := a.ClaimPrepared(ctx, prepared, "tool-parent-owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	permit, err := a.BeginPrepared(ctx, owned)
	if err != nil {
		t.Fatal(err)
	}
	response := []byte(`{"tool_requests":[{"name":"get_changed_files"}]}`)
	if err := a.RecordOutcome(ctx, permit, orchestrator.DispatchOutcome{Response: response, ResponseReceived: true, ResponseDigest: hash(response), ElapsedMS: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RecoverActionResults(ctx, 8); err != nil {
		t.Fatal(err)
	}
	if n, err := a.RecoverModelReductions(ctx, 8, pipeline.ModelReducerV1{}); err != nil || n != 1 {
		t.Fatalf("reduce change %s: %d %v", change, n, err)
	}
	return i
}

func TestPostgresCompetingSnapshotObservationsShareLastProjectCapacity(t *testing.T) {
	db, _ := postgresFixture(t)
	limits := ledgerLimits()
	limits.Project.ToolCalls = 1
	a := ledgerAuthority(t, db, limits)
	first := toolParentForChange(t, a, "7")
	second := toolParentForChange(t, a, "8")
	observer := &countedToolObserver{}
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, actionID := range []string{first.ActionID, second.ActionID} {
		go func(id string) {
			<-start
			_, err := a.CommitModelToolObservations(context.Background(), id, observer)
			errs <- err
		}(actionID)
	}
	close(start)
	accepted, denied := 0, 0
	for n := 0; n < 2; n++ {
		if err := <-errs; err == nil {
			accepted++
		} else if strings.Contains(err.Error(), "project budget exhausted") {
			denied++
		} else {
			t.Fatal(err)
		}
	}
	if accepted != 1 || denied != 1 || observer.calls.Load() != 1 {
		t.Fatalf("last shared allowance was spent twice: accepted=%d denied=%d renders=%d", accepted, denied, observer.calls.Load())
	}
	var records, links, holds int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM budget_model_tool_observations),(SELECT count(*) FROM budget_tool_observation_scopes),(SELECT count(*) FROM budget_holds)`).Scan(&records, &links, &holds); err != nil || records != 1 || links != 3 || holds != 6 {
		t.Fatalf("competing acceptance lost atomic accounting: %d %d %d %v", records, links, holds, err)
	}
}

func TestPostgresSealedToolObservationsRequireIntactRecoveryCursor(t *testing.T) {
	for _, mode := range []string{"live", "recovered", "mixed", "missing_cursor"} {
		t.Run(mode, func(t *testing.T) {
			a, intents := modelBatchFixture(t)
			ctx := context.Background()
			prepared, err := a.PrepareModelBatch(ctx, intents)
			if err != nil {
				t.Fatal(err)
			}
			for index, action := range prepared {
				var claim orchestrator.ActionClaim
				if mode == "live" || (mode == "mixed" && index == 0) {
					claim, err = a.ClaimPrepared(ctx, action, "sealed-live-owner", time.Minute)
				} else {
					claim, err = a.ReclaimPrepared(ctx, action, "sealed-recovery-owner", time.Minute)
				}
				if err != nil {
					t.Fatal(err)
				}
				permit, err := a.BeginPrepared(ctx, claim)
				if err != nil {
					t.Fatal(err)
				}
				response := []byte(`{"tool_requests":[{"name":"get_changed_files"}]}`)
				if err := a.RecordOutcome(ctx, permit, orchestrator.DispatchOutcome{Response: response, ResponseReceived: true, ResponseDigest: hash(response), ElapsedMS: 1}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := a.RecoverActionResults(ctx, 8); err != nil {
				t.Fatal(err)
			}
			if n, err := a.RecoverModelReductions(ctx, 8, pipeline.ModelReducerV1{}); err != nil || n != 1 {
				t.Fatalf("sealed tool reduction: %d %v", n, err)
			}
			if mode == "missing_cursor" {
				if _, err := a.db.Exec(`UPDATE budget_jobs SET recovery_action_id=NULL`); err != nil {
					t.Fatal(err)
				}
			}
			observer := &countedToolObserver{}
			artifact, err := a.CommitModelToolObservations(ctx, prepared[0].ID, observer)
			if mode == "missing_cursor" {
				if err == nil || artifact != nil || observer.calls.Load() != 0 {
					t.Fatalf("lost cursor was guessed from a sending epoch: %#v calls=%d %v", artifact, observer.calls.Load(), err)
				}
			} else if err != nil || len(artifact.Observations) != len(prepared) || observer.calls.Load() != 1 {
				t.Fatalf("qualified sealed parent rejected: %#v calls=%d %v", artifact, observer.calls.Load(), err)
			}
		})
	}
}
