package accounting

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
)

func cancellationFixture(t *testing.T) (*Authority, orchestrator.DispatchIntent, review.OperatorActor, review.CancelAttemptCommand, string) {
	t.Helper()
	a, i, dsn := preparedActionFixture(t)
	actor, c := cancellationActor(t, a, i)
	return a, i, actor, c, dsn
}

func cancellationActor(t *testing.T, a *Authority, i orchestrator.DispatchIntent) (review.OperatorActor, review.CancelAttemptCommand) {
	t.Helper()
	raw := `{"schema_version":1,"principals":[{"id":"alice","token_env":"ALICE_TOKEN","grants":[{"boundary_id":"` + a.cfg.BoundaryID + `","project_id":"` + a.cfg.ProjectID + `","provider":"` + i.Attempt.Change.Provider + `","host":"` + i.Attempt.Change.Host + `","repository_id":"` + i.Attempt.Change.RepositoryID + `","roles":["operator"]}]}]}`
	d, err := review.DecodeOperatorDirectory([]byte(raw), func(string) string { return "alice-secret" })
	if err != nil {
		t.Fatal(err)
	}
	a.cfg.Operators = d
	actor, _ := d.Authenticate("alice-secret")
	c := review.CancelAttemptCommand{SchemaVersion: 1, CommandID: "cancel-1", AttemptID: i.Attempt.AttemptID, ExpectedVersion: 1, Revision: i.Attempt.Snapshot.AnalysisRevision(), Kind: "cancel_attempt", Payload: review.CancelAttemptPayload{Reason: "operator stop"}}
	return actor, c
}

func TestCancellationFencesPreparedOwnerAndReplaysOneDurableReceipt(t *testing.T) {
	a, i, actor, c, dsn := cancellationFixture(t)
	ctx := context.Background()
	p, err := a.PrepareAction(ctx, i)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := a.ClaimPrepared(ctx, p, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := a.CancelAttempt(ctx, actor, c)
	if err != nil || receipt == nil || receipt.ControlVersion != 2 || receipt.State != "cancelled" || receipt.ReservationDisposition != "retained" || receipt.ActorID != "alice" {
		t.Fatalf("cancel: %+v %v", receipt, err)
	}
	if _, err = a.BeginPrepared(ctx, claim); err == nil {
		t.Fatal("stale owner began after cancellation")
	}
	if _, err = a.ReclaimPrepared(ctx, p, "recovery", time.Minute); err == nil {
		t.Fatal("cancelled preparation reclaimed")
	}
	if err = a.FinishInbox(ctx, i.Job.RunID, i.Job.Token, true); err == nil {
		t.Fatal("old worker completed cancelled job")
	}
	v, err := a.OperatorAttempt(ctx, actor, c.AttemptID)
	if err != nil || v.Active || v.InboxState != "cancelled" || v.ControlVersion != 2 {
		t.Fatalf("status: %+v %v", v, err)
	}
	var holds int
	var state string
	if err = a.db.QueryRow(`SELECT state,(SELECT count(*) FROM budget_holds) FROM budget_actions WHERE id=$1`, p.ID).Scan(&state, &holds); err != nil || state != "prepared" || holds != 3 {
		t.Fatalf("cancellation erased uncertain reservation: %s %d %v", state, holds, err)
	}
	if candidates, err := a.PreparedRecoveryCandidates(ctx, 10); err != nil || len(candidates) != 0 {
		t.Fatalf("cancelled candidate rediscovered: %+v %v", candidates, err)
	}
	if err := reconciler(a).SettleFinal(ctx, finalReceipt(i)); err == nil {
		t.Fatal("prepared cancellation was mistaken for provider usage/refund proof")
	}
	db, err := OpenDatabase(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	restarted, err := NewAuthority(db, a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	again, err := restarted.CancelAttempt(ctx, actor, c)
	if err != nil || !reflect.DeepEqual(receipt, again) {
		t.Fatalf("lost-ack replay: %+v %v", again, err)
	}
	c.Payload.Reason = "different command"
	if _, err = a.CancelAttempt(ctx, actor, c); !errors.Is(err, ErrOperatorCommandConflict) {
		t.Fatalf("conflicting reuse: %v", err)
	}
}

func TestCancellationRejectsStaleUnauthenticatedAndCorruptCommands(t *testing.T) {
	a, i, actor, c, _ := cancellationFixture(t)
	ctx := context.Background()
	if _, err := a.CancelAttempt(ctx, review.OperatorActor{}, c); !errors.Is(err, review.ErrOperatorUnauthorized) {
		t.Fatalf("forged actor: %v", err)
	}
	for _, mutate := range []func(*review.CancelAttemptCommand){func(c *review.CancelAttemptCommand) { c.ExpectedVersion = 2 }, func(c *review.CancelAttemptCommand) { c.Revision = "other" }} {
		stale := c
		mutate(&stale)
		if _, err := a.CancelAttempt(ctx, actor, stale); !errors.Is(err, ErrOperatorCommandConflict) {
			t.Fatalf("stale command: %v", err)
		}
	}
	var count int
	if err := a.db.QueryRow(`SELECT count(*) FROM budget_operator_commands`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed command persisted: %d %v", count, err)
	}
	if _, err := a.db.Exec(`UPDATE budget_aggregates SET checkpoint_digest='corrupt' WHERE run_id=$1`, i.Job.RunID); err != nil {
		t.Fatal(err)
	}
	if r, err := a.CancelAttempt(ctx, actor, c); err == nil || r != nil {
		t.Fatalf("corrupt aggregate granted cancellation: %+v %v", r, err)
	}
}

func TestCancellationDuplicateWorkersCommitOnlyOneDisposition(t *testing.T) {
	a, _, actor, c, _ := cancellationFixture(t)
	var wg sync.WaitGroup
	results := make(chan *review.CancelAttemptReceipt, 8)
	start := make(chan struct{})
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r, err := a.CancelAttempt(context.Background(), actor, c)
			if err != nil {
				t.Error(err)
				return
			}
			results <- r
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var first *review.CancelAttemptReceipt
	for r := range results {
		if first == nil {
			first = r
		}
		if !reflect.DeepEqual(first, r) {
			t.Error("duplicate workers received different receipts")
		}
	}
	var rows int
	var version uint64
	if err := a.db.QueryRow(`SELECT (SELECT count(*) FROM budget_operator_commands),control_version FROM budget_jobs`).Scan(&rows, &version); err != nil || rows != 1 || version != 2 {
		t.Fatalf("duplicate disposition: %d %d %v", rows, version, err)
	}
}

func TestCancellationCommitFailureRetainsOwnerAndLateSendingOutcome(t *testing.T) {
	a, i, actor, c, _ := cancellationFixture(t)
	ctx := context.Background()
	p, err := a.PrepareAction(ctx, i)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := a.ClaimPrepared(ctx, p, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(`CREATE FUNCTION fail_cancel_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected cancellation failure'; END $$; CREATE CONSTRAINT TRIGGER fail_cancel_commit AFTER INSERT ON budget_operator_commands DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_cancel_commit()`); err != nil {
		t.Fatal(err)
	}
	if r, err := a.CancelAttempt(ctx, actor, c); err == nil || r != nil {
		t.Fatalf("uncommitted receipt escaped: %+v %v", r, err)
	}
	permit, err := a.BeginPrepared(ctx, claim)
	if err != nil {
		t.Fatalf("failed cancellation fenced owner: %v", err)
	}
	if _, err = a.db.Exec(`DROP TRIGGER fail_cancel_commit ON budget_operator_commands`); err != nil {
		t.Fatal(err)
	}
	if _, err = a.CancelAttempt(ctx, actor, c); err != nil {
		t.Fatal(err)
	}
	body := []byte("late response")
	if err = a.RecordOutcome(ctx, permit, orchestrator.DispatchOutcome{ResponseReceived: true, Response: body, ResponseDigest: hash(body), ElapsedMS: 1}); err != nil {
		t.Fatalf("late attributable result rejected: %v", err)
	}
	var state string
	var settled bool
	var holds int
	if err = a.db.QueryRow(`SELECT state,settled,(SELECT count(*) FROM budget_holds) FROM budget_actions WHERE id=$1`, p.ID).Scan(&state, &settled, &holds); err != nil || state != "result_recorded" || settled || holds != 3 {
		t.Fatalf("sending spend refunded/lost: %s %v %d %v", state, settled, holds, err)
	}
	item, err := a.LookupInbox(ctx, i.Job.RunID)
	if err != nil || item.State != "cancelled" {
		t.Fatalf("late result revived job: %+v %v", item, err)
	}
	for n := 0; n < 2; n++ {
		if err := reconciler(a).SettleFinal(ctx, finalReceipt(i)); err != nil {
			t.Fatalf("qualified late settlement rejected: %v", err)
		}
	}
	item, err = a.LookupInbox(ctx, i.Job.RunID)
	if err != nil || item.State != "cancelled" {
		t.Fatalf("settlement revived job: %+v %v", item, err)
	}
	if _, err = a.BeginPrepared(ctx, claim); err == nil {
		t.Fatal("settlement restored dispatch")
	}
}

func TestCancellationRacesSendingWithoutLosingAttribution(t *testing.T) {
	for n := 0; n < 6; n++ {
		a, i, actor, c, _ := cancellationFixture(t)
		ctx := context.Background()
		p, err := a.PrepareAction(ctx, i)
		if err != nil {
			t.Fatal(err)
		}
		claim, err := a.ClaimPrepared(ctx, p, "worker", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		sent := make(chan orchestrator.DispatchPermit, 1)
		done := make(chan error, 1)
		go func() { <-start; p, _ := a.BeginPrepared(ctx, claim); sent <- p }()
		go func() { <-start; _, err := a.CancelAttempt(ctx, actor, c); done <- err }()
		close(start)
		permit := <-sent
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		var state string
		var marker bool
		if err = a.db.QueryRow(`SELECT a.state,l.sending_fence IS NOT NULL FROM budget_actions a JOIN budget_action_leases l ON l.action_id=a.id WHERE a.id=$1`, p.ID).Scan(&state, &marker); err != nil {
			t.Fatal(err)
		}
		if permit.ID == "" {
			if marker || state != "prepared" {
				t.Fatal("rejected sender left a marker")
			}
		} else {
			if !marker || state != "sending" {
				t.Fatal("successful sender lost durable attribution")
			}
			body := []byte("late race response")
			if err = a.RecordOutcome(ctx, permit, orchestrator.DispatchOutcome{ResponseReceived: true, Response: body, ResponseDigest: hash(body), ElapsedMS: 1}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err = a.BeginPrepared(ctx, claim); err == nil {
			t.Fatal("sender obtained new permit after cancellation")
		}
	}
}

func TestCancellationFencesInterruptedPreparedReclaim(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "expired"}[expired], func(t *testing.T) {
			a, i, actor, c, _ := cancellationFixture(t)
			ctx := context.Background()
			p, err := a.PrepareAction(ctx, i)
			if err != nil {
				t.Fatal(err)
			}
			claim, err := a.ClaimPrepared(ctx, p, "old", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = a.db.Exec(`UPDATE budget_action_leases SET lease_until=clock_timestamp()-interval '1 second'`); err != nil {
				t.Fatal(err)
			}
			p.Fence = claim.Fence
			recovered, err := a.ReclaimPrepared(ctx, p, "recovery", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if expired {
				if _, err = a.db.Exec(`UPDATE budget_inbox SET deadline=clock_timestamp()-interval '1 second'`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = a.CancelAttempt(ctx, actor, c); err != nil {
				t.Fatalf("recovered work could not be cancelled: %v", err)
			}
			if _, err = a.BeginPrepared(ctx, recovered); err == nil {
				t.Fatal("interrupted recovery owner resumed after cancellation")
			}
			if _, err = a.BeginPrepared(ctx, claim); err == nil {
				t.Fatal("original owner resumed after cancellation")
			}
		})
	}
}

func TestCancellationAndModelTerminalCompletionHaveOneWinner(t *testing.T) {
	for n := 0; n < 4; n++ {
		a, i, _ := recordedReductionFixture(t)
		actor, c := cancellationActor(t, a, i)
		ctx := context.Background()
		if _, err := a.RecoverModelReductions(ctx, 128, &countedModelReducer{}); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		cancelled := make(chan error, 1)
		completed := make(chan error, 1)
		go func() { <-start; _, err := a.CancelAttempt(ctx, actor, c); cancelled <- err }()
		go func() { <-start; _, err := a.CompleteModelJob(ctx, i.ActionID); completed <- err }()
		close(start)
		cancelErr, completeErr := <-cancelled, <-completed
		if (cancelErr == nil) == (completeErr == nil) {
			t.Fatalf("terminal race did not have one winner: cancel=%v complete=%v", cancelErr, completeErr)
		}
		v, err := a.OperatorAttempt(ctx, actor, c.AttemptID)
		if err != nil || v.ControlVersion != 2 {
			t.Fatalf("terminal control version: %+v %v", v, err)
		}
		if cancelErr == nil && v.InboxState != "cancelled" {
			t.Fatal("completion overwrote cancellation")
		}
		if completeErr == nil && v.InboxState != "completed" {
			t.Fatal("cancellation overwrote completion")
		}
	}
}

func TestCancellationTargetsOriginalJobNotDuplicateAlias(t *testing.T) {
	a, i, actor, c, _ := cancellationFixture(t)
	ctx := context.Background()
	req, input, source := preparedAggregate(t, a)
	req.DeliveryID = "duplicate-alias"
	input.Request = req
	input.DeliveryID = req.DeliveryID
	input.ExpectedGeneration = 1
	source.Request = req
	alias, err := a.AcceptAggregate(ctx, req, input, source, nil)
	if err != nil || alias.AttemptID != c.AttemptID || alias.RunID == i.Job.RunID {
		t.Fatalf("duplicate alias: %+v %v", alias, err)
	}
	if _, err = a.CancelAttempt(ctx, actor, c); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 4; n++ {
		v, err := a.OperatorAttempt(ctx, actor, c.AttemptID)
		if err != nil || v.RunID != i.Job.RunID || v.InboxState != "cancelled" {
			t.Fatalf("alias changed control target: %+v %v", v, err)
		}
		if _, err = a.CancelAttempt(ctx, actor, c); err != nil {
			t.Fatalf("alias broke receipt replay: %v", err)
		}
	}
	aliasView, err := a.LookupInbox(ctx, alias.RunID)
	if err != nil || aliasView.State != "cancelled" {
		t.Fatalf("duplicate alias concealed cancellation: %+v %v", aliasView, err)
	}
	health, err := a.CheckInbox(ctx)
	if err != nil || health.Pending != 0 {
		t.Fatalf("cancelled alias remained pending: %+v %v", health, err)
	}
	if next, err := a.ClaimInbox(ctx); err != nil || next != nil {
		t.Fatalf("cancelled alias queued another job: %+v %v", next, err)
	}
}

func TestCancellationReceiptCannotBeReplayedAsAnotherActorOrAfterRevocation(t *testing.T) {
	a, i, _, c, _ := cancellationFixture(t)
	grant := map[string]any{"boundary_id": a.cfg.BoundaryID, "project_id": a.cfg.ProjectID, "provider": i.Attempt.Change.Provider, "host": i.Attempt.Change.Host, "repository_id": i.Attempt.Change.RepositoryID, "roles": []string{"operator"}}
	publisherGrant := map[string]any{}
	for key, value := range grant {
		publisherGrant[key] = value
	}
	publisherGrant["roles"] = []string{"publisher"}
	raw, _ := json.Marshal(map[string]any{"schema_version": 1, "principals": []any{
		map[string]any{"id": "alice", "token_env": "ALICE_TOKEN", "grants": []any{grant}},
		map[string]any{"id": "bob", "token_env": "BOB_TOKEN", "grants": []any{grant}},
		map[string]any{"id": "publisher", "token_env": "PUBLISHER_TOKEN", "grants": []any{publisherGrant}},
	}})
	d, err := review.DecodeOperatorDirectory(raw, func(key string) string { return key + "-secret" })
	if err != nil {
		t.Fatal(err)
	}
	a.cfg.Operators = d
	alice, _ := d.Authenticate("ALICE_TOKEN-secret")
	bob, _ := d.Authenticate("BOB_TOKEN-secret")
	publisher, _ := d.Authenticate("PUBLISHER_TOKEN-secret")
	if _, err = a.CancelAttempt(context.Background(), publisher, c); !errors.Is(err, review.ErrOperatorForbidden) {
		t.Fatalf("publication role granted cancellation: %v", err)
	}
	if _, err = a.CancelAttempt(context.Background(), alice, c); err != nil {
		t.Fatal(err)
	}
	if _, err = a.CancelAttempt(context.Background(), bob, c); !errors.Is(err, ErrOperatorCommandConflict) {
		t.Fatalf("command actor was rebound: %v", err)
	}
	a.cfg.Operators = nil
	if _, err = a.CancelAttempt(context.Background(), alice, c); !errors.Is(err, review.ErrOperatorUnauthorized) {
		t.Fatalf("revoked authority replayed command: %v", err)
	}
	a.cfg.Operators = d
	if _, err = a.db.Exec(`UPDATE budget_operator_commands SET receipt_digest='corrupt'`); err != nil {
		t.Fatal(err)
	}
	if r, err := a.CancelAttempt(context.Background(), alice, c); !errors.Is(err, ErrIntegrity) || r != nil {
		t.Fatalf("corrupt receipt replayed: %+v %v", r, err)
	}
}

func TestCancellationFencesAlreadyClaimedDuplicateAlias(t *testing.T) {
	a, _, actor, c, _ := cancellationFixture(t)
	ctx := context.Background()
	req, input, source := preparedAggregate(t, a)
	req.DeliveryID = "claimed-duplicate"
	input.Request = req
	input.DeliveryID = req.DeliveryID
	input.ExpectedGeneration = 1
	source.Request = req
	alias, err := a.AcceptAggregate(ctx, req, input, source, nil)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := a.ClaimInbox(ctx)
	if err != nil || claim == nil || claim.RunID != alias.RunID {
		t.Fatalf("duplicate claim: %+v %v", claim, err)
	}
	if _, err = a.CancelAttempt(ctx, actor, c); err != nil {
		t.Fatal(err)
	}
	if err = a.FinishInbox(ctx, claim.RunID, claim.ClaimToken, true); err == nil {
		t.Fatal("stale alias owner completed cancelled attempt")
	}
	view, err := a.LookupInbox(ctx, claim.RunID)
	if err != nil || view.State != "cancelled" {
		t.Fatalf("claimed alias concealed cancellation: %+v %v", view, err)
	}
	health, err := a.CheckInbox(ctx)
	if err != nil || health.Running != 0 || health.Pending != 0 {
		t.Fatalf("cancelled aliases remained active: %+v %v", health, err)
	}
}

func TestCancellationOfSupersededAttemptReturnsScopedCurrentIdentity(t *testing.T) {
	a, _, actor, c, _ := cancellationFixture(t)
	ctx := context.Background()
	req, input, source := preparedAggregate(t, a)
	req.DeliveryID = "new-manual-intent"
	input.Request = req
	input.DeliveryID = req.DeliveryID
	input.ExpectedGeneration = 1
	input.IntentKind = review.IntentManual
	source.Request = req
	current, err := a.AcceptAggregate(ctx, req, input, source, nil)
	if err != nil || current.AttemptID == c.AttemptID {
		t.Fatalf("new manual intent: %+v %v", current, err)
	}
	_, err = a.CancelAttempt(ctx, actor, c)
	var conflict *OperatorCommandConflict
	if !errors.As(err, &conflict) || conflict.CurrentAttemptID != current.AttemptID || conflict.ControlVersion != 1 {
		t.Fatalf("stale target lost current identity: %+v %v", conflict, err)
	}
	c.AttemptID = current.AttemptID
	c.CommandID = "cancel-current"
	if _, err = a.CancelAttempt(ctx, actor, c); err != nil {
		t.Fatal(err)
	}
}

func TestCancellationCrashChild(t *testing.T) {
	dsn := os.Getenv("SEVENREVIEW_CANCELLATION_DSN")
	if dsn == "" {
		t.Skip("subprocess helper")
	}
	db, err := OpenDatabase(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	id := os.Getenv("SEVENREVIEW_CANCELLATION_ATTEMPT")
	if _, err = db.Exec(`SELECT set_config('application_name',$1,false)`, "cancel-"+id); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	var b attemptBinding
	if err = db.QueryRow(`SELECT binding FROM budget_attempts WHERE id=$1`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	a := ledgerAuthority(t, db, ledgerLimits())
	actor, c := cancellationActor(t, a, orchestrator.DispatchIntent{Attempt: b.Attempt})
	if _, err = a.CancelAttempt(context.Background(), actor, c); err != nil {
		t.Fatal(err)
	}
	os.Exit(73)
}

func TestCancellationProcessDeathBeforeAndAfterCommit(t *testing.T) {
	for _, stage := range []string{"before_commit", "commit_ack_unknown", "after_commit"} {
		t.Run(stage, func(t *testing.T) {
			a, i, actor, c, dsn := cancellationFixture(t)
			ctx := context.Background()
			p, err := a.PrepareAction(ctx, i)
			if err != nil {
				t.Fatal(err)
			}
			claim, err := a.ClaimPrepared(ctx, p, "worker", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			lock, err := a.db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			if stage != "after_commit" {
				if _, err = lock.ExecContext(ctx, `SELECT pg_advisory_lock(hashtext(current_schema()))`); err != nil {
					t.Fatal(err)
				}
				defer lock.ExecContext(ctx, `SELECT pg_advisory_unlock(hashtext(current_schema()))`)
				if _, err = a.db.Exec(`CREATE FUNCTION pause_cancel_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(hashtext(TG_TABLE_SCHEMA)); RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER pause_cancel_commit AFTER INSERT ON budget_operator_commands DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pause_cancel_commit()`); err != nil {
					t.Fatal(err)
				}
			}
			child := exec.Command(os.Args[0], "-test.run=^TestCancellationCrashChild$")
			child.Env = append(os.Environ(), "SEVENREVIEW_CANCELLATION_DSN="+dsn, "SEVENREVIEW_CANCELLATION_ATTEMPT="+c.AttemptID)
			if err = child.Start(); err != nil {
				t.Fatal(err)
			}
			defer child.Process.Kill()
			if stage != "after_commit" {
				waiting := false
				deadline := time.Now().Add(10 * time.Second)
				for time.Now().Before(deadline) {
					if err = a.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event='advisory')`, "cancel-"+c.AttemptID).Scan(&waiting); err != nil {
						t.Fatal(err)
					}
					if waiting {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if !waiting {
					t.Fatal("child never reached the uncommitted cancellation barrier")
				}
				if err = child.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				_ = child.Wait()
				if stage == "before_commit" {
					// Client death alone does not prove COMMIT rolled back. Kill the
					// blocked backend to establish this side of the durable boundary.
					var terminated bool
					if err = a.db.QueryRow(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name=$1 AND wait_event='advisory'`, "cancel-"+c.AttemptID).Scan(&terminated); err != nil || !terminated {
						t.Fatalf("could not interrupt cancellation backend: %v", err)
					}
				}
				if _, err = lock.ExecContext(ctx, `SELECT pg_advisory_unlock(hashtext(current_schema()))`); err != nil {
					t.Fatal(err)
				}
				if stage == "commit_ack_unknown" {
					// Reconcile either possible commit outcome by exact command ID.
					r, err := a.CancelAttempt(ctx, actor, c)
					if err != nil || r.ControlVersion != 2 {
						t.Fatalf("unknown commit outcome failed reconciliation: %+v %v", r, err)
					}
					var count int
					if err = a.db.QueryRow(`SELECT count(*) FROM budget_operator_commands`).Scan(&count); err != nil || count != 1 {
						t.Fatalf("unknown commit duplicated command: %d %v", count, err)
					}
					if _, err = a.BeginPrepared(ctx, claim); err == nil {
						t.Fatal("reconciled cancellation left dispatch authority")
					}
					return
				}
				beginCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				if _, err = a.BeginPrepared(beginCtx, claim); err != nil {
					t.Fatalf("dead cancellation fenced original owner: %v", err)
				}
				var count int
				if err = a.db.QueryRow(`SELECT count(*) FROM budget_operator_commands`).Scan(&count); err != nil || count != 0 {
					t.Fatalf("uncommitted audit survived death: %d %v", count, err)
				}
			} else {
				err = child.Wait()
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 73 {
					t.Fatalf("child failed before committed receipt: %v", err)
				}
				if _, err = a.BeginPrepared(ctx, claim); err == nil {
					t.Fatal("committed cancellation lost after process death")
				}
				r, err := a.CancelAttempt(ctx, actor, c)
				if err != nil || r.ControlVersion != 2 {
					t.Fatalf("committed cancellation replay failed: %+v %v", r, err)
				}
			}
		})
	}
}
