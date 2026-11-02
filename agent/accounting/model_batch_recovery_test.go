package accounting

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/pipeline"
)

func recordBatchResponse(t *testing.T, a *Authority, c orchestrator.ActionClaim) {
	t.Helper()
	p, err := a.BeginPrepared(context.Background(), c)
	if err != nil || p.AlreadyStarted {
		t.Fatalf("fresh batch send: %#v %v", p, err)
	}
	raw := []byte("[]")
	if err = a.RecordOutcome(context.Background(), p, orchestrator.DispatchOutcome{Response: raw, ResponseReceived: true, ResponseDigest: hash(raw), ElapsedMS: 1}); err != nil {
		t.Fatal(err)
	}
}

func TestSealedBatchRecoveryContinuesExactSlotsAcrossRestart(t *testing.T) {
	for _, liveFirst := range []bool{false, true} {
		name := "prepared_restart"
		if liveFirst {
			name = "live_result_then_restart"
		}
		t.Run(name, func(t *testing.T) {
			a, intents := modelBatchFixture(t)
			ctx := context.Background()
			prepared, err := a.PrepareModelBatch(ctx, intents)
			if err != nil {
				t.Fatal(err)
			}
			start := 0
			if liveFirst {
				c, err := a.ClaimPrepared(ctx, prepared[0], "live", time.Minute)
				if err != nil {
					t.Fatal(err)
				}
				recordBatchResponse(t, a, c)
				start = 1
			}
			token := ""
			for index := start; index < len(prepared); index++ {
				reopened := ledgerAuthority(t, a.db, ledgerLimits())
				c, err := reopened.ReclaimPrepared(ctx, prepared[index], "batch-recovery", time.Minute)
				if err != nil {
					t.Fatal(err)
				}
				if c.Job == nil || c.Job.Token == intents[0].Job.Token || (token != "" && token != c.Job.Token) {
					t.Fatalf("batch epoch changed or live owner survived: %#v", c.Job)
				}
				token = c.Job.Token
				recordBatchResponse(t, reopened, c)
			}
			if err = a.FinishInbox(ctx, intents[0].Job.RunID, intents[0].Job.Token, true); err == nil {
				t.Fatal("revoked live owner completed recovered batch")
			}
			if _, err = a.RecoverActionResults(ctx, 8); err != nil {
				t.Fatal(err)
			}
			if _, err = a.RecoverModelReductions(ctx, 128, pipeline.ModelReducerV1{}); err != nil {
				t.Fatal(err)
			}
			if p, err := a.CompleteModelJob(ctx, prepared[0].ID); err != nil || p == nil || !p.Succeeded {
				t.Fatalf("recovered batch cannot finish: %#v %v", p, err)
			}
			var actions, holds, settled int
			if err = a.db.QueryRow(`SELECT (SELECT count(*) FROM budget_actions),(SELECT count(*) FROM budget_holds),(SELECT count(*) FROM budget_actions WHERE settled)`).Scan(&actions, &holds, &settled); err != nil || actions != 2 || holds != 6 || settled != 0 {
				t.Fatalf("recovery replayed or refunded: %d %d %d %v", actions, holds, settled, err)
			}
		})
	}
}

func TestSealedBatchRecoveryFencesDuplicateAndExpiredOwners(t *testing.T) {
	a, intents := modelBatchFixture(t)
	ctx := context.Background()
	prepared, err := a.PrepareModelBatch(ctx, intents)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	claims := make(chan orchestrator.ActionClaim, 2)
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := a.ReclaimPrepared(ctx, prepared[0], "contender", time.Minute)
			if err == nil {
				claims <- c
			}
		}()
	}
	wg.Wait()
	close(claims)
	var winner orchestrator.ActionClaim
	count := 0
	for c := range claims {
		winner = c
		count++
	}
	if count != 1 {
		t.Fatalf("duplicate batch owners: %d", count)
	}
	if _, err = a.db.Exec(`UPDATE budget_action_leases SET lease_until=clock_timestamp()-interval '1 second' WHERE action_id=$1`, winner.ID); err != nil {
		t.Fatal(err)
	}
	next, err := a.ReclaimPrepared(ctx, orchestrator.PreparedAction{ID: winner.ID, RequestDigest: winner.RequestDigest, Fence: winner.Fence}, "replacement", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if next.Job.Token != winner.Job.Token || next.Fence != winner.Fence+1 {
		t.Fatal("expired worker changed batch epoch or retained fence")
	}
	if p, err := a.BeginPrepared(ctx, winner); err == nil && !p.AlreadyStarted {
		t.Fatal("expired batch owner sent")
	}
	recordBatchResponse(t, a, next)
	second, err := a.ReclaimPrepared(ctx, prepared[1], "next-slot", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if second.Job.Token != next.Job.Token {
		t.Fatal("moving batch cursor revoked proven result epoch")
	}
	if _, err = a.ReclaimPrepared(ctx, orchestrator.PreparedAction{ID: winner.ID, RequestDigest: winner.RequestDigest, Fence: next.Fence}, "old-slot", time.Minute); err == nil {
		t.Fatal("recorded slot was reclaimed")
	}
}

type batchRecoveryProvider struct {
	t      *testing.T
	a      *Authority
	intent orchestrator.DispatchIntent
	calls  atomic.Int32
}

func (p *batchRecoveryProvider) Name() string { return p.intent.Provider }
func (p *batchRecoveryProvider) Complete(_ context.Context, req orchestrator.LLMRequest) (string, error) {
	raw, _ := json.Marshal(req)
	if !bytes.Equal(raw, p.intent.Payload) {
		p.t.Error("batch recovery changed request")
	}
	var sending int
	if err := p.a.db.QueryRow(`SELECT count(*) FROM budget_actions a JOIN budget_action_leases l ON l.action_id=a.id WHERE a.state='sending' AND l.sending_fence=l.fence`).Scan(&sending); err != nil || sending != 1 {
		p.t.Errorf("batch I/O without unique committed sending fence: %d %v", sending, err)
	}
	p.calls.Add(1)
	return "[]", nil
}
func batchRecoveryOrchestrator(a *Authority, p *batchRecoveryProvider) *orchestrator.Orchestrator {
	limits := p.intent.OperatorCeilings.Clone()
	return orchestrator.NewOrchestrator(orchestrator.DefaultOrchestratorConfig("not-original", "not-original", p.Name()), map[string]orchestrator.LLMProvider{p.Name(): p}, orchestrator.WithBudgetAuthority(a), orchestrator.WithOperatorCeilings(&limits))
}

func TestSealedBatchAutomaticRecoveryUsesOneCursorWithDuplicateWorkers(t *testing.T) {
	for _, liveFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "prepared", true: "live_result"}[liveFirst], func(t *testing.T) {
			a, intents := modelBatchFixture(t)
			ctx := context.Background()
			prepared, err := a.PrepareModelBatch(ctx, intents)
			if err != nil {
				t.Fatal(err)
			}
			if liveFirst {
				c, err := a.ClaimPrepared(ctx, prepared[0], "live-owner", time.Minute)
				if err != nil {
					t.Fatal(err)
				}
				recordBatchResponse(t, a, c)
			}
			provider := &batchRecoveryProvider{t: t, a: a, intent: intents[0]}
			for round := 0; round < 4; round++ {
				var wg sync.WaitGroup
				for n := 0; n < 2; n++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						reopened := ledgerAuthority(t, a.db, ledgerLimits())
						_, _ = batchRecoveryOrchestrator(reopened, provider).RecoverPreparedActions(ctx, 8)
					}()
				}
				wg.Wait()
			}
			want := int32(2)
			if liveFirst {
				want = 1
			}
			if provider.calls.Load() != want {
				t.Fatalf("automatic batch calls=%d want=%d", provider.calls.Load(), want)
			}
			if _, err = a.RecoverActionResults(ctx, 8); err != nil {
				t.Fatal(err)
			}
			if _, err = a.RecoverModelReductions(ctx, 128, pipeline.ModelReducerV1{}); err != nil {
				t.Fatal(err)
			}
			if n, err := a.CompletePendingModelJobs(ctx, 8); err != nil || n != 1 {
				t.Fatalf("automatic finalizer stranded batch: %d %v", n, err)
			}
			if _, err = a.LookupModelJob(ctx, intents[0].Job.RunID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBatchRecoveryCrashChild(t *testing.T) {
	stage := os.Getenv("SEVENREVIEW_BATCH_CRASH")
	if stage == "" {
		t.Skip("subprocess helper")
	}
	db, err := sql.Open("pgx", os.Getenv("SEVENREVIEW_BATCH_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	a := ledgerAuthority(t, db, ledgerLimits())
	var prepared orchestrator.PreparedAction
	if err = json.Unmarshal([]byte(os.Getenv("SEVENREVIEW_BATCH_ACTION")), &prepared); err != nil {
		t.Fatal(err)
	}
	lease := time.Minute
	if stage == "claim" {
		lease = 20 * time.Millisecond
	}
	claim, err := a.ReclaimPrepared(context.Background(), prepared, "crashed-recovery", lease)
	if err != nil {
		t.Fatal(err)
	}
	if stage == "claim" {
		os.Exit(74)
	}
	permit, err := a.BeginPrepared(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if stage == "sending" {
		os.Exit(74)
	}
	raw := []byte("[]")
	if err = a.RecordOutcome(context.Background(), permit, orchestrator.DispatchOutcome{Response: raw, ResponseReceived: true, ResponseDigest: hash(raw), ElapsedMS: 1}); err != nil {
		t.Fatal(err)
	}
	os.Exit(74)
}

func TestSealedBatchProcessDeathNeverReplaysSendingOrRecordedSlots(t *testing.T) {
	for _, stage := range []string{"claim", "sending", "result"} {
		t.Run(stage, func(t *testing.T) {
			a, intents, dsn := modelBatchFixtureDSN(t)
			ctx := context.Background()
			prepared, err := a.PrepareModelBatch(ctx, intents)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(prepared[0])
			child := exec.Command(os.Args[0], "-test.run=^TestBatchRecoveryCrashChild$")
			child.Env = append(os.Environ(), "SEVENREVIEW_BATCH_CRASH="+stage, "SEVENREVIEW_BATCH_DSN="+dsn, "SEVENREVIEW_BATCH_ACTION="+string(raw))
			output, childErr := child.CombinedOutput()
			if err = childErr; err == nil {
				t.Fatal("child did not exit at crash boundary")
			} else if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 74 {
				t.Fatalf("child missed boundary: %v %s", err, output)
			}
			if stage == "claim" {
				if _, err = a.db.Exec(`SELECT pg_sleep(greatest(0,extract(epoch FROM (lease_until-clock_timestamp())))+0.002) FROM budget_action_leases WHERE action_id=$1`, prepared[0].ID); err != nil {
					t.Fatal(err)
				}
			}
			provider := &batchRecoveryProvider{t: t, a: a, intent: intents[0]}
			for pass := 0; pass < 4; pass++ {
				reopened := ledgerAuthority(t, a.db, ledgerLimits())
				if _, err = batchRecoveryOrchestrator(reopened, provider).RecoverPreparedActions(ctx, 8); err != nil {
					t.Fatal(err)
				}
			}
			want := map[string]int32{"claim": 2, "sending": 0, "result": 1}[stage]
			if provider.calls.Load() != want {
				t.Fatalf("replayed crash boundary: calls=%d want=%d", provider.calls.Load(), want)
			}
			var actions, holds, settled int
			if err = a.db.QueryRow(`SELECT (SELECT count(*) FROM budget_actions),(SELECT count(*) FROM budget_holds),(SELECT count(*) FROM budget_actions WHERE settled)`).Scan(&actions, &holds, &settled); err != nil || actions != 2 || holds != 6 || settled != 0 {
				t.Fatalf("crash reset effects or holds: %d %d %d %v", actions, holds, settled, err)
			}
		})
	}
}

func TestSealedBatchRecoveryCommitFailurePreservesLiveAuthority(t *testing.T) {
	a, intents := modelBatchFixture(t)
	ctx := context.Background()
	prepared, err := a.PrepareModelBatch(ctx, intents)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(`CREATE FUNCTION fail_batch_reclaim() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected reclaim commit failure'; END $$; CREATE CONSTRAINT TRIGGER fail_batch_reclaim AFTER UPDATE ON budget_action_leases DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_batch_reclaim()`); err != nil {
		t.Fatal(err)
	}
	if c, err := a.ReclaimPrepared(ctx, prepared[0], "failed-recovery", time.Minute); err == nil || c.Job != nil {
		t.Fatalf("uncommitted cursor escaped: %#v %v", c, err)
	}
	var token string
	var cursor sql.NullString
	var fence int
	if err = a.db.QueryRow(`SELECT i.claim_token,j.recovery_action_id,l.fence FROM budget_inbox i JOIN budget_jobs j USING(run_id) JOIN budget_action_leases l ON l.action_id=$1`, prepared[0].ID).Scan(&token, &cursor, &fence); err != nil {
		t.Fatal(err)
	}
	if token != intents[0].Job.Token || cursor.Valid || fence != 0 {
		t.Fatal("failed reclaim changed epoch/cursor/fence")
	}
}

func TestSealedBatchRecoveryRejectsUnprovenOrStillOwnedProgress(t *testing.T) {
	for _, fault := range []string{"skip", "live_sibling_lease", "foreign_result_epoch", "zero_result_fence", "namespace", "deadline", "closed"} {
		t.Run(fault, func(t *testing.T) {
			a, intents := modelBatchFixture(t)
			ctx := context.Background()
			prepared, err := a.PrepareModelBatch(ctx, intents)
			if err != nil {
				t.Fatal(err)
			}
			target := prepared[0]
			switch fault {
			case "skip":
				target = prepared[1]
			case "live_sibling_lease":
				_, err = a.ClaimPrepared(ctx, prepared[1], "still-live", time.Minute)
			case "foreign_result_epoch", "zero_result_fence":
				c, e := a.ClaimPrepared(ctx, prepared[0], "live", time.Minute)
				if e != nil {
					t.Fatal(e)
				}
				recordBatchResponse(t, a, c)
				if fault == "foreign_result_epoch" {
					_, err = a.db.Exec(`UPDATE budget_action_leases SET job_token='unproven' WHERE action_id=$1`, prepared[0].ID)
				} else {
					_, err = a.db.Exec(`UPDATE budget_action_leases SET fence=0,sending_fence=0,worker_id=NULL,lease_until=NULL WHERE action_id=$1`, prepared[0].ID)
				}
				target = prepared[1]
			case "namespace":
				a.cfg.ProjectID = "foreign-project"
			case "deadline":
				_, err = a.db.Exec(`UPDATE budget_inbox SET deadline=clock_timestamp()-interval '1 second'`)
			case "closed":
				err = a.CloseAttempt(ctx, intents[0].Attempt.AttemptID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if c, err := a.ReclaimPrepared(ctx, target, "not-authorized", time.Minute); err == nil || c.Job != nil {
				t.Fatalf("unsafe batch progress claimed: %#v %v", c, err)
			}
			var token string
			var grant sql.NullString
			if err = a.db.QueryRow(`SELECT i.claim_token,j.recovery_action_id FROM budget_inbox i JOIN budget_jobs j USING(run_id)`).Scan(&token, &grant); err != nil {
				t.Fatal(err)
			}
			if token != intents[0].Job.Token || grant.Valid {
				t.Fatal("rejected takeover changed live job authority")
			}
			if fault == "namespace" || fault == "deadline" || fault == "closed" || fault == "live_sibling_lease" {
				if candidates, err := a.PreparedRecoveryCandidates(ctx, 8); err != nil || len(candidates) != 0 {
					t.Fatalf("ineligible batch leaked into automatic scan: %#v %v", candidates, err)
				}
			}
		})
	}
}

func TestSealedBatchTerminalResultCannotUseAnUnboundCurrentEpoch(t *testing.T) {
	a, intents := modelBatchFixture(t)
	ctx := context.Background()
	prepared, err := a.PrepareModelBatch(ctx, intents)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range prepared {
		c, err := a.ClaimPrepared(ctx, p, "live", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		recordBatchResponse(t, a, c)
	}
	if _, err = a.RecoverActionResults(ctx, 8); err != nil {
		t.Fatal(err)
	}
	if _, err = a.RecoverModelReductions(ctx, 128, pipeline.ModelReducerV1{}); err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(`UPDATE budget_inbox SET claim_token='unbound-epoch'`); err != nil {
		t.Fatal(err)
	}
	if p, err := a.CompleteModelJob(ctx, prepared[0].ID); err == nil || p != nil {
		t.Fatalf("original results cleared an unbound epoch: %#v %v", p, err)
	}
}

func TestSealedBatchTerminalRequiresRealSendingFence(t *testing.T) {
	a, intents := modelBatchFixture(t)
	ctx := context.Background()
	prepared, err := a.PrepareModelBatch(ctx, intents)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range prepared {
		c, err := a.ClaimPrepared(ctx, p, "live", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		recordBatchResponse(t, a, c)
	}
	if _, err = a.RecoverActionResults(ctx, 8); err != nil {
		t.Fatal(err)
	}
	if _, err = a.RecoverModelReductions(ctx, 128, pipeline.ModelReducerV1{}); err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(`UPDATE budget_action_leases SET fence=0,sending_fence=0,worker_id=NULL,lease_until=NULL WHERE action_id=$1`, prepared[0].ID); err != nil {
		t.Fatal(err)
	}
	if p, err := a.CompleteModelJob(ctx, prepared[0].ID); err == nil || p != nil {
		t.Fatalf("zero fence completed batch: %#v %v", p, err)
	}
}
