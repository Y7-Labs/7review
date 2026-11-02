package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/review"
)

func TestInboxAutomaticallyReleasesCancelledUnusedPreparationAfterRestartWithoutProviderIO(t *testing.T) {
	s, db, dsn := inboxServerDSN(t)
	s.cfg.Operators = appOperators(t)
	accepted := inboxWebhook(t, s, inboxBody, "cancel-unused-restart")
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("accept: %d %s", accepted.Code, accepted.Body.String())
	}
	var receipt requestReviewResult
	if err := json.Unmarshal(accepted.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	a, p, build := qualifiedToolService(t, s, db)
	s.pipeline.Orchestrator = build(crashAfterPreparation{a}, "pinned")
	item, err := a.ClaimInbox(context.Background())
	if err != nil || item == nil {
		t.Fatalf("claim: %+v %v", item, err)
	}
	ctx := review.WithJobClaim(context.Background(), review.JobClaim{RunID: item.RunID, Token: item.ClaimToken})
	if err = s.runInboxItem(ctx, item.Request, item.RunID); !errors.Is(err, errInboxPanic) {
		t.Fatalf("did not lose frame after durable preparation: %v", err)
	}
	actor, err := s.cfg.Operators.Authenticate("ALICE_TOKEN-secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.CancelAttempt(context.Background(), actor, review.CancelAttemptCommand{SchemaVersion: 1, CommandID: "cancel-unused", AttemptID: receipt.AttemptID, ExpectedVersion: 1, Revision: "head", Kind: "cancel_attempt", Payload: review.CancelAttemptPayload{Reason: "operator stop"}}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	restarted := newInboxServer(t, dsn)
	// Its default live route catalog is empty. Cleanup uses frozen evidence only.
	restarted.startInboxWorkers()
	deadline := time.Now().Add(5 * time.Second)
	released := false
	for time.Now().Before(deadline) {
		if err = db.QueryRow(`SELECT EXISTS(SELECT 1 FROM budget_actions WHERE state='not_executed')`).Scan(&released); err != nil {
			t.Fatal(err)
		}
		if released {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !released {
		t.Fatal("cancelled unused preparation was not recovered")
	}
	restarted.Close()
	var proofs, holds int
	var version uint64
	var state string
	var unowned bool
	if err = db.QueryRow(`SELECT (SELECT count(*) FROM budget_unused_releases),(SELECT count(*) FROM budget_holds),j.control_version,i.state,i.claim_token IS NULL FROM budget_jobs j JOIN budget_inbox i USING(run_id)`).Scan(&proofs, &holds, &version, &state, &unowned); err != nil || proofs != 1 || holds != 3 || version != 3 || state != "cancelled" || !unowned || p.calls.Load() != 0 {
		t.Fatalf("cleanup resumed/sent/recharged work: proof=%d holds=%d version=%d state=%s unowned=%v calls=%d err=%v", proofs, holds, version, state, unowned, p.calls.Load(), err)
	}
}

func TestInboxAutomaticallyReleasesSupersededUnusedPreparationAfterRestartWithoutProviderIO(t *testing.T) {
	s, db, dsn := inboxServerDSN(t)
	accepted := inboxWebhook(t, s, inboxBody, "superseded-unused-old")
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("accept old: %d %s", accepted.Code, accepted.Body.String())
	}
	a, p, build := qualifiedToolService(t, s, db)
	s.pipeline.Orchestrator = build(crashAfterPreparation{a}, "pinned")
	item, err := a.ClaimInbox(context.Background())
	if err != nil || item == nil {
		t.Fatalf("claim old: %+v %v", item, err)
	}
	ctx := review.WithJobClaim(context.Background(), review.JobClaim{RunID: item.RunID, Token: item.ClaimToken})
	if err = s.runInboxItem(ctx, item.Request, item.RunID); !errors.Is(err, errInboxPanic) {
		t.Fatalf("did not lose old frame after durable preparation: %v", err)
	}
	newBody := strings.Replace(inboxBody, `"head":{"sha":"head"}`, `"head":{"sha":"head-next"}`, 1)
	superseding := inboxWebhook(t, s, newBody, "superseded-unused-new")
	if superseding.Code != http.StatusAccepted {
		t.Fatalf("accept successor: %d %s", superseding.Code, superseding.Body.String())
	}
	s.Close()
	restarted := newInboxServer(t, dsn)
	restarted.startInboxWorkers()
	deadline := time.Now().Add(5 * time.Second)
	var released bool
	for time.Now().Before(deadline) {
		if err = db.QueryRow(`SELECT EXISTS(SELECT 1 FROM budget_actions WHERE state='not_executed')`).Scan(&released); err != nil {
			t.Fatal(err)
		}
		if released {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	restarted.Close()
	if !released {
		t.Fatal("superseded unused preparation was not recovered")
	}
	var cause, state, phase string
	var holds, proofs int
	var dispatch, unowned bool
	if err = db.QueryRow(`SELECT r.cause,i.state,j.phase,j.dispatch_allowed,i.claim_token IS NULL,(SELECT count(*) FROM budget_holds WHERE action_id=r.action_id),(SELECT count(*) FROM budget_unused_releases) FROM budget_unused_releases r JOIN budget_actions a ON a.id=r.action_id JOIN budget_jobs j ON j.attempt_id=a.attempt_id JOIN budget_inbox i USING(run_id) WHERE convert_from(j.receipt,'UTF8')::jsonb->>'Duplicate'='false'`).Scan(&cause, &state, &phase, &dispatch, &unowned, &holds, &proofs); err != nil || cause != "superseded" || state != "superseded" || phase != "finished" || dispatch || !unowned || holds != 3 || proofs != 1 || p.calls.Load() != 0 {
		t.Fatalf("supersession cleanup resumed/sent/recharged work: %s %s %s %v %v %d %d calls=%d err=%v", cause, state, phase, dispatch, unowned, holds, proofs, p.calls.Load(), err)
	}
}

func TestInboxDoesNotClaimFreshWorkWhileUnusedReleaseJournalIsUnavailable(t *testing.T) {
	s, db, _ := inboxServerDSN(t)
	accepted := inboxWebhook(t, s, inboxBody, "unused-journal-unavailable")
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("accept: %d %s", accepted.Code, accepted.Body.String())
	}
	if _, err := db.Exec(`ALTER TABLE budget_unused_releases RENAME TO unavailable_unused_releases`); err != nil {
		t.Fatal(err)
	}
	s.startInboxWorkers()
	time.Sleep(500 * time.Millisecond)
	var state string
	var unowned bool
	if err := db.QueryRow(`SELECT state,claim_token IS NULL FROM budget_inbox`).Scan(&state, &unowned); err != nil || state != "pending" || !unowned {
		t.Fatalf("dispatch bypassed missing release journal: %s %v %v", state, unowned, err)
	}
	if _, err := db.Exec(`ALTER TABLE unavailable_unused_releases RENAME TO budget_unused_releases`); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := db.QueryRow(`SELECT state FROM budget_inbox`).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != "pending" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("workers did not resume after the journal was restored")
}
