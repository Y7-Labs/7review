package accounting

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
)

func linkedAction(t *testing.T) (*Authority, orchestrator.DispatchIntent, orchestrator.DispatchPermit, string, string) {
	t.Helper()
	db, dsn := postgresFixture(t)
	l := ledgerLimits()
	a := ledgerAuthority(t, db, l)
	i := ledgerIntent(t, db, "fixture", "7", l)
	input := admissionFromIntent(i)
	if _, err := a.AcceptInbox(context.Background(), input.Request, time.Minute); err != nil {
		t.Fatal(err)
	}
	claim, err := a.ClaimInbox(context.Background())
	if err != nil || claim == nil {
		t.Fatalf("claim: %#v %v", claim, err)
	}
	input.Execution.DeadlineAt = claim.Deadline
	input.Job = &review.JobClaim{RunID: claim.RunID, Token: claim.ClaimToken}
	receipt, err := a.Admit(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	i = dispatchForReceipt(i, receipt)
	i.Job = input.Job
	permit, err := a.ReserveAndBegin(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	return a, i, permit, claim.RunID, dsn
}

func TestPostgresRecordedActionRecoveryReplaysOnlyLocalReduction(t *testing.T) {
	a, i, permit, runID, _ := linkedAction(t)
	ctx := context.Background()
	// Sending without a definitive outcome is never treated as a local result.
	if n, err := a.RecoverActionResults(ctx, 32); err != nil || n != 0 {
		t.Fatalf("sending action was recovered as result: %d %v", n, err)
	}
	response := []byte(`{"findings":[]}`)
	outcome := orchestrator.DispatchOutcome{Response: response, ResponseReceived: true, ResponseDigest: hash(response), ElapsedMS: 1}
	if err := a.RecordOutcome(ctx, permit, outcome); err != nil {
		t.Fatal(err)
	}
	_, err := a.db.Exec(`CREATE FUNCTION fail_result_reduction() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected local reduction commit failure'; END $$; CREATE CONSTRAINT TRIGGER fail_result_reduction AFTER INSERT ON budget_action_results DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_result_reduction()`)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := a.RecoverActionResults(ctx, 32); err == nil || n != 0 {
		t.Fatalf("uncommitted reduction confirmed: %d %v", n, err)
	}
	if _, err = a.db.Exec(`DROP TRIGGER fail_result_reduction ON budget_action_results`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	counts := make(chan int, 8)
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, e := a.RecoverActionResults(ctx, 32)
			if e != nil {
				t.Error(e)
			}
			counts <- n
		}()
	}
	wg.Wait()
	close(counts)
	total := 0
	for n := range counts {
		total += n
	}
	if total != 1 {
		t.Fatalf("local outcome reduced %d times", total)
	}
	var digest, state string
	var holds, actions int
	if err = a.db.QueryRow(`SELECT response_digest,status FROM budget_action_results WHERE action_id=$1 AND run_id=$2`, i.ActionID, runID).Scan(&digest, &state); err != nil || digest != outcome.ResponseDigest || state != "recorded_unvalidated" {
		t.Fatalf("result lost provenance or fabricated validation: %s %s %v", digest, state, err)
	}
	_ = a.db.QueryRow(`SELECT count(*) FROM budget_holds`).Scan(&holds)
	_ = a.db.QueryRow(`SELECT count(*) FROM budget_actions`).Scan(&actions)
	if holds != 3 || actions != 1 {
		t.Fatalf("local recovery changed spend: holds=%d actions=%d", holds, actions)
	}
	status, err := a.LookupInbox(ctx, runID)
	if err != nil || status.RecordedUnvalidated != 1 || status.State != "running" || status.AttemptID != i.Attempt.AttemptID {
		t.Fatalf("recovery fabricated completion or lost linkage: %#v %v", status, err)
	}
	replay, err := a.ReserveAndBegin(ctx, i)
	if err != nil || !replay.AlreadyStarted {
		t.Fatalf("reduction permitted model resend: %#v %v", replay, err)
	}
}

func TestPostgresActionRecoveryCrashChild(t *testing.T) {
	mode := os.Getenv("SEVENREVIEW_RESULT_CRASH_CHILD")
	if mode == "" {
		t.Skip("subprocess helper")
	}
	db, err := sql.Open("pgx", os.Getenv("SEVENREVIEW_RESULT_CRASH_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	a := ledgerAuthority(t, db, ledgerLimits())
	var permit orchestrator.DispatchPermit
	if err = json.Unmarshal([]byte(os.Getenv("SEVENREVIEW_RESULT_CRASH_PERMIT")), &permit); err != nil {
		t.Fatal(err)
	}
	response := []byte("committed response")
	if err = a.RecordOutcome(context.Background(), permit, orchestrator.DispatchOutcome{Response: response, ResponseReceived: true, ResponseDigest: hash(response), ElapsedMS: 1}); err != nil {
		t.Fatal(err)
	}
	if mode == "reduced" {
		if n, err := a.RecoverActionResults(context.Background(), 32); err != nil || n != 1 {
			t.Fatalf("local reduction: %d %v", n, err)
		}
	}
	os.Exit(41)
}

func TestPostgresProcessDeathAfterOutcomeAndReductionDoesNotResend(t *testing.T) {
	for _, mode := range []string{"recorded", "reduced"} {
		t.Run(mode, func(t *testing.T) {
			a, i, permit, runID, dsn := linkedAction(t)
			raw, _ := json.Marshal(permit)
			cmd := exec.Command(os.Args[0], "-test.run=^TestPostgresActionRecoveryCrashChild$")
			cmd.Env = append(os.Environ(), "SEVENREVIEW_RESULT_CRASH_CHILD="+mode, "SEVENREVIEW_RESULT_CRASH_DSN="+dsn, "SEVENREVIEW_RESULT_CRASH_PERMIT="+string(raw))
			err := cmd.Run()
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 41 {
				t.Fatalf("child did not reach crash checkpoint: %v", err)
			}
			reopened := ledgerAuthority(t, a.db, ledgerLimits())
			n, err := reopened.RecoverActionResults(context.Background(), 32)
			want := 1
			if mode == "reduced" {
				want = 0
			}
			if err != nil || n != want {
				t.Fatalf("crash lost result or repeated reduction: %d %v", n, err)
			}
			status, err := reopened.LookupInbox(context.Background(), runID)
			if err != nil || status.RecordedUnvalidated != 1 || status.State == "completed" {
				t.Fatalf("recovery fabricated completion: %#v %v", status, err)
			}
			replay, err := reopened.ReserveAndBegin(context.Background(), i)
			if err != nil || !replay.AlreadyStarted {
				t.Fatalf("crash granted another send: %#v %v", replay, err)
			}
		})
	}
}

func TestPostgresRecoveryDoesNotPromoteUnknownOrCorruptOutcomes(t *testing.T) {
	for _, name := range []string{"unknown", "corrupt"} {
		t.Run(name, func(t *testing.T) {
			a, i, p, _, _ := linkedAction(t)
			response := []byte("response")
			out := orchestrator.DispatchOutcome{Response: response, ResponseReceived: name != "unknown", ResponseDigest: hash(response), ElapsedMS: 1}
			if err := a.RecordOutcome(context.Background(), p, out); err != nil {
				t.Fatal(err)
			}
			if name == "corrupt" {
				out.ResponseDigest = "sha256:" + string64("0")
				raw, _ := json.Marshal(out)
				if _, err := a.db.Exec(`UPDATE budget_actions SET outcome=$2 WHERE id=$1`, i.ActionID, raw); err != nil {
					t.Fatal(err)
				}
			}
			n, err := a.RecoverActionResults(context.Background(), 32)
			if n != 0 || (name == "corrupt" && err == nil) || (name == "unknown" && err != nil) {
				t.Fatalf("invalid evidence promoted: %d %v", n, err)
			}
			var results, holds int
			_ = a.db.QueryRow(`SELECT count(*) FROM budget_action_results`).Scan(&results)
			_ = a.db.QueryRow(`SELECT count(*) FROM budget_holds`).Scan(&holds)
			if results != 0 || holds != 3 {
				t.Fatalf("unknown/corrupt outcome changed evidence or accounting: results=%d holds=%d", results, holds)
			}
		})
	}
}
