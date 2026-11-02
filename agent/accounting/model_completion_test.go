package accounting

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/pipeline"
	"github.com/Y4NN777/7review/agent/review"
)

func TestModelCompletionCrashChild(t *testing.T) {
	if os.Getenv("SEVENREVIEW_COMPLETION_DSN") == "" {
		t.Skip("subprocess helper")
	}
	db, err := sql.Open("pgx", os.Getenv("SEVENREVIEW_COMPLETION_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`SELECT set_config('application_name',$1,false)`, os.Getenv("SEVENREVIEW_COMPLETION_ACTION")); err != nil {
		t.Fatal(err)
	}
	a := ledgerAuthority(t, db, ledgerLimits())
	if _, err = a.CompleteModelJob(context.Background(), os.Getenv("SEVENREVIEW_COMPLETION_ACTION")); err != nil {
		t.Fatal(err)
	}
	os.Exit(73)
}

func TestModelCompletionProcessDeathBeforeAndAfterCommit(t *testing.T) {
	for _, stage := range []string{"before_commit", "after_commit"} {
		t.Run(stage, func(t *testing.T) {
			a, i, _, dsn := recordedReductionFixtureDSN(t)
			ctx := context.Background()
			if _, err := a.RecoverModelReductions(ctx, 128, pipeline.ModelReducerV1{}); err != nil {
				t.Fatal(err)
			}
			if stage == "before_commit" {
				lock, err := a.db.Conn(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
				if _, err = lock.ExecContext(ctx, `SELECT pg_advisory_lock(hashtext(current_schema()))`); err != nil {
					t.Fatal(err)
				}
				defer lock.ExecContext(ctx, `SELECT pg_advisory_unlock(hashtext(current_schema()))`)
				if _, err = a.db.Exec(`CREATE FUNCTION pause_completion_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(hashtext(TG_TABLE_SCHEMA)); RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER pause_completion_commit AFTER UPDATE ON budget_jobs DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pause_completion_commit()`); err != nil {
					t.Fatal(err)
				}
			}
			child := exec.Command(os.Args[0], "-test.run=^TestModelCompletionCrashChild$")
			child.Env = append(os.Environ(), "SEVENREVIEW_COMPLETION_DSN="+dsn, "SEVENREVIEW_COMPLETION_ACTION="+i.ActionID)
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			defer child.Process.Kill()
			if stage == "before_commit" {
				waiting := false
				deadline := time.Now().Add(10 * time.Second)
				for time.Now().Before(deadline) {
					if err := a.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event='advisory')`, i.ActionID).Scan(&waiting); err != nil {
						t.Fatal(err)
					}
					if waiting {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if !waiting {
					t.Fatal("child did not reach deferred commit barrier")
				}
				if err := child.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				if err := child.Wait(); err == nil {
					t.Fatal("child survived interruption")
				}
				// A dead client may leave COMMIT waiting in PostgreSQL. Terminate
				// that backend too to establish the rolled-back side of the boundary.
				var terminated bool
				if err := a.db.QueryRow(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name=$1 AND wait_event='advisory'`, i.ActionID).Scan(&terminated); err != nil || !terminated {
					t.Fatalf("could not interrupt commit backend: %v", err)
				}
				if _, err := a.db.Exec(`DROP TRIGGER pause_completion_commit ON budget_jobs`); err != nil {
					t.Fatal(err)
				}
				if p, err := a.LookupModelJob(ctx, i.Job.RunID); !errors.Is(err, ErrModelJobNotFound) || p != nil {
					t.Fatalf("uncommitted projection escaped: %#v %v", p, err)
				}
			} else if err := child.Wait(); err == nil {
				t.Fatal("child did not exit at committed boundary")
			} else if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 73 {
				t.Fatalf("child failed before boundary: %v", err)
			}
			reopened := ledgerAuthority(t, a.db, ledgerLimits())
			if p, err := reopened.CompleteModelJob(ctx, i.ActionID); err != nil || !p.Succeeded {
				t.Fatalf("local restart failed: %#v %v", p, err)
			}
			var actions, holds, settled int
			if err := a.db.QueryRow(`SELECT (SELECT count(*) FROM budget_actions),(SELECT count(*) FROM budget_holds),(SELECT count(*) FROM budget_actions WHERE settled)`).Scan(&actions, &holds, &settled); err != nil {
				t.Fatal(err)
			}
			if actions != 1 || holds != 3 || settled != 0 {
				t.Fatal("restart replayed or refunded model action")
			}
		})
	}
}

func TestModelCompletionRechecksFormatterAncestry(t *testing.T) {
	for _, completed := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_completion", true: "after_completion"}[completed], func(t *testing.T) {
			a, parent, _ := formatterParentFixture(t)
			ctx := context.Background()
			child, err := a.AdvanceModelContinuation(ctx, parent.ActionID, pipeline.ModelContinuationV1{})
			if err != nil {
				t.Fatal(err)
			}
			claim, err := a.ReclaimPrepared(ctx, child, "formatter", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			permit, err := a.BeginPrepared(ctx, claim)
			if err != nil {
				t.Fatal(err)
			}
			raw := []byte("[]")
			if err = a.RecordOutcome(ctx, permit, orchestrator.DispatchOutcome{Response: raw, ResponseReceived: true, ResponseDigest: hash(raw), ElapsedMS: 1}); err != nil {
				t.Fatal(err)
			}
			if _, err = a.RecoverActionResults(ctx, 8); err != nil {
				t.Fatal(err)
			}
			if _, err = a.RecoverModelReductions(ctx, 128, pipeline.ModelReducerV1{}); err != nil {
				t.Fatal(err)
			}
			if completed {
				if _, err = a.CompleteModelJob(ctx, child.ID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = a.db.Exec(`UPDATE budget_model_reductions SET artifact_digest='revoked' WHERE id=(SELECT parent_reduction_id FROM budget_model_continuations WHERE child_action_id=$1)`, child.ID); err != nil {
				t.Fatal(err)
			}
			if p, err := a.CompleteModelJob(ctx, child.ID); err == nil || p != nil {
				t.Fatalf("changed ancestry completed: %#v %v", p, err)
			}
			if completed {
				if p, err := a.LookupModelJob(ctx, parent.Job.RunID); err == nil || p != nil {
					t.Fatalf("changed ancestry served: %#v %v", p, err)
				}
			}
		})
	}
}

func TestModelCompletionIsAtomicDurableAndNotApproval(t *testing.T) {
	a, i, claim := recordedReductionFixture(t)
	ctx := context.Background()
	if _, err := a.RecoverModelReductions(ctx, 128, &countedModelReducer{}); err != nil {
		t.Fatal(err)
	}
	_, err := a.db.Exec(`CREATE FUNCTION fail_model_completion() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.terminal_reduction_id IS NOT NULL THEN RAISE EXCEPTION 'injected completion commit failure'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER fail_model_completion AFTER UPDATE ON budget_jobs DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_model_completion()`)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := a.CompleteModelJob(ctx, i.ActionID); err == nil || result != nil {
		t.Fatalf("uncommitted terminal projection escaped: %#v %v", result, err)
	}
	var state, phase string
	var allowed bool
	if err = a.db.QueryRow(`SELECT i.state,j.phase,j.dispatch_allowed FROM budget_inbox i JOIN budget_jobs j USING(run_id)`).Scan(&state, &phase, &allowed); err != nil {
		t.Fatal(err)
	}
	if state != "running" || phase != "effect_started" || !allowed {
		t.Fatal("failed completion changed job authority")
	}
	if _, err = a.db.Exec(`DROP TRIGGER fail_model_completion ON budget_jobs`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan *review.ModelJobProjection, 2)
	errs := make(chan error, 2)
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func() { defer wg.Done(); p, e := a.CompleteModelJob(ctx, i.ActionID); results <- p; errs <- e }()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for result := range results {
		if result == nil || !result.Succeeded || result.RunID != i.Job.RunID || result.Source.Report.Draft == "" || result.Source.Report.Final != "" {
			t.Fatalf("bad terminal projection: %#v", result)
		}
	}
	if err = a.FinishInbox(ctx, i.Job.RunID, claim.Token, true); err == nil {
		t.Fatal("old worker overwrote terminal projection")
	}
	reopened := ledgerAuthority(t, a.db, ledgerLimits())
	projection, err := reopened.LookupModelJob(ctx, i.Job.RunID)
	if err != nil || projection.Source.Model.ParseStatus != "empty_findings" || projection.CompletedAt.IsZero() {
		t.Fatalf("restart lost terminal projection: %#v %v", projection, err)
	}
	if err = a.db.QueryRow(`SELECT i.state,j.phase,j.dispatch_allowed FROM budget_inbox i JOIN budget_jobs j USING(run_id)`).Scan(&state, &phase, &allowed); err != nil {
		t.Fatal(err)
	}
	if state != "completed" || phase != "finished" || allowed {
		t.Fatalf("terminal job still dispatchable: %s %s %v", state, phase, allowed)
	}
	var holds, settled int
	if err = a.db.QueryRow(`SELECT count(*) FROM budget_holds`).Scan(&holds); err != nil {
		t.Fatal(err)
	}
	if err = a.db.QueryRow(`SELECT count(*) FROM budget_actions WHERE settled`).Scan(&settled); err != nil {
		t.Fatal(err)
	}
	if holds != 3 || settled != 0 {
		t.Fatal("completion released or settled unknown usage")
	}
}

func TestModelCompletionRejectsStaleOrUnresolvedAuthority(t *testing.T) {
	for _, fault := range []string{"epoch", "deadline", "closed", "namespace", "sibling", "reduced_sibling"} {
		t.Run(fault, func(t *testing.T) {
			a, i, _ := recordedReductionFixture(t)
			ctx := context.Background()
			if _, err := a.RecoverModelReductions(ctx, 128, &countedModelReducer{}); err != nil {
				t.Fatal(err)
			}
			var err error
			switch fault {
			case "epoch":
				_, err = a.db.Exec(`UPDATE budget_inbox SET claim_token='other-epoch'`)
			case "deadline":
				_, err = a.db.Exec(`UPDATE budget_inbox SET deadline=clock_timestamp()-interval '1 second'`)
			case "closed":
				err = a.CloseAttempt(ctx, i.Attempt.AttemptID)
			case "namespace":
				a.cfg.ProjectID = "other"
			case "sibling":
				_, err = a.db.Exec(`INSERT INTO budget_actions(id,attempt_id,request_digest,intent_digest,request,quote,upper_amount,state) SELECT 'unknown-sibling',attempt_id,request_digest,intent_digest,request,quote,upper_amount,'uncertain' FROM budget_actions LIMIT 1`)
			case "reduced_sibling":
				_, err = a.db.Exec(`INSERT INTO budget_actions(id,attempt_id,request_digest,intent_digest,request,quote,upper_amount,state) SELECT 'unlinked-sibling',attempt_id,request_digest,intent_digest,request,quote,upper_amount,'result_recorded' FROM budget_actions LIMIT 1; INSERT INTO budget_action_results(action_id,run_id,request_digest,response_digest,outcome,status) SELECT 'unlinked-sibling',run_id,request_digest,response_digest,outcome,status FROM budget_action_results LIMIT 1; INSERT INTO budget_model_reductions(id,run_id,attempt_id,group_digest,input_digest,artifact_digest,artifact,state) SELECT 'other-reduction',run_id,attempt_id,'other-group',input_digest,artifact_digest,artifact,state FROM budget_model_reductions LIMIT 1; INSERT INTO budget_model_reduction_inputs(action_id,reduction_id) VALUES('unlinked-sibling','other-reduction')`)
			}
			if err != nil {
				t.Fatal(err)
			}
			if result, err := a.CompleteModelJob(ctx, i.ActionID); err == nil || result != nil {
				t.Fatalf("invalid authority completed: %#v %v", result, err)
			}
		})
	}
}

func TestBlockedModelResultCompletesFailedWithoutApprovalOrRefund(t *testing.T) {
	a, i, _, _ := recordedReductionFixtureResponse(t, []byte(""))
	ctx := context.Background()
	if _, err := a.RecoverModelReductions(ctx, 128, &countedModelReducer{}); err != nil {
		t.Fatal(err)
	}
	p, err := a.CompleteModelJob(ctx, i.ActionID)
	if err != nil || p == nil || p.Succeeded || p.Reason == "" || p.Source.Report.Final != "" || p.Source.Report.Draft != "" {
		t.Fatalf("blocked result became success: %#v %v", p, err)
	}
	var state string
	var settled int
	if err = a.db.QueryRow(`SELECT state FROM budget_inbox`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err = a.db.QueryRow(`SELECT count(*) FROM budget_actions WHERE settled`).Scan(&settled); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || settled != 0 {
		t.Fatal("failed terminal result changed financial state")
	}
}
