package accounting

import (
	"context"
	"database/sql"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
)

func supersededPreparedFixture(t *testing.T) (*Authority, orchestrator.PreparedAction, orchestrator.ActionClaim, string) {
	t.Helper()
	a, intent, dsn := preparedActionFixture(t)
	ctx := context.Background()
	prepared, err := a.PrepareAction(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := a.ClaimPrepared(ctx, prepared, "superseded-owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	req, admission, source := preparedAggregate(t, a)
	req.DeliveryID = "superseding-delivery"
	req.SourceSHA = "superseding-head"
	admission.Request = req
	admission.DeliveryID = req.DeliveryID
	admission.ExpectedGeneration = intent.Attempt.Generation
	admission.IntentKind = review.IntentManual
	scm := *source.SCM
	scm.DiffRefs.HeadSHA = req.SourceSHA
	source.SCM = &scm
	source.Request = req
	_, attestation, err := review.AttestSCMRequest(req, &scm, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	admission.Attestation = attestation
	admission.Execution.ComparisonTree = attestation.ComparisonTree
	source.Attestation = &attestation
	if _, err = a.AcceptAggregate(ctx, req, admission, source, nil); err != nil {
		t.Fatal(err)
	}
	return a, prepared, claim, dsn
}

func TestSupersededPreparedReleaseUsesExactSuccessorProofAndSurvivesRestart(t *testing.T) {
	a, prepared, staleClaim, dsn := supersededPreparedFixture(t)
	ctx := context.Background()
	r, err := a.ReleaseUnusedPrepared(ctx, prepared.ID)
	if err != nil || r == nil || r.Cause != "superseded" || r.SupersedingAttemptID == "" || r.SupersessionIntakeID == "" || !canonicalDigest(r.SupersessionReceiptDigest) {
		t.Fatalf("superseded release: %+v %v", r, err)
	}
	if _, err = a.BeginPrepared(ctx, staleClaim); err == nil {
		t.Fatal("stale superseded owner obtained sending authority")
	}
	var state, inbox, phase string
	var dispatch, unowned bool
	var holds, proofs int
	if err = a.db.QueryRow(`SELECT a.state,i.state,j.phase,j.dispatch_allowed,i.claim_token IS NULL,(SELECT count(*) FROM budget_holds),(SELECT count(*) FROM budget_unused_releases) FROM budget_actions a JOIN budget_jobs j ON j.attempt_id=a.attempt_id JOIN budget_inbox i USING(run_id) WHERE a.id=$1 AND convert_from(j.receipt,'UTF8')::jsonb->>'Duplicate'='false'`, prepared.ID).Scan(&state, &inbox, &phase, &dispatch, &unowned, &holds, &proofs); err != nil || state != "not_executed" || inbox != "superseded" || phase != "finished" || dispatch || !unowned || holds != 3 || proofs != 1 {
		t.Fatalf("superseded disposition: %s %s %s %v %v %d %d %v", state, inbox, phase, dispatch, unowned, holds, proofs, err)
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
	again, err := restarted.ReleaseUnusedPrepared(ctx, prepared.ID)
	if err != nil || !reflect.DeepEqual(r, again) {
		t.Fatalf("restart changed supersession proof: %+v %v", again, err)
	}
}

func TestSupersededPreparedAutomaticRecoveryIsDuplicateSafeAndNeverSends(t *testing.T) {
	a, prepared, _, _ := supersededPreparedFixture(t)
	ctx := context.Background()
	const workers = 8
	var wg sync.WaitGroup
	results := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := a.RecoverUnusedPrepared(ctx, 8)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	var proofs, links int
	var state string
	var sending sql.NullInt64
	if err := a.db.QueryRow(`SELECT a.state,l.sending_fence,(SELECT count(*) FROM budget_unused_releases),(SELECT count(*) FROM budget_unused_release_scopes) FROM budget_actions a JOIN budget_action_leases l ON l.action_id=a.id WHERE a.id=$1`, prepared.ID).Scan(&state, &sending, &proofs, &links); err != nil || state != "not_executed" || sending.Valid || proofs != 1 || links != 3 {
		t.Fatalf("duplicate recovery changed action: %s %+v %d %d %v", state, sending, proofs, links, err)
	}
	if n, err := a.RecoverUnusedPrepared(ctx, 8); err != nil || n != 0 {
		t.Fatalf("completed proof rediscovered: %d %v", n, err)
	}
}

func TestSupersededPreparedReleaseCommitFailureRollsBackWholeDisposition(t *testing.T) {
	a, prepared, _, _ := supersededPreparedFixture(t)
	ctx := context.Background()
	if _, err := a.db.Exec(`CREATE FUNCTION fail_superseded_release() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected superseded release failure'; END $$; CREATE CONSTRAINT TRIGGER fail_superseded_release AFTER INSERT ON budget_unused_releases DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_superseded_release()`); err != nil {
		t.Fatal(err)
	}
	if r, err := a.ReleaseUnusedPrepared(ctx, prepared.ID); err == nil || r != nil {
		t.Fatal("failed supersession release acknowledged")
	}
	var action, inbox, phase string
	var dispatch, owned bool
	var proofs, links int
	if err := a.db.QueryRow(`SELECT a.state,i.state,j.phase,j.dispatch_allowed,i.claim_token IS NOT NULL,(SELECT count(*) FROM budget_unused_releases),(SELECT count(*) FROM budget_unused_release_scopes) FROM budget_actions a JOIN budget_jobs j ON j.attempt_id=a.attempt_id JOIN budget_inbox i USING(run_id) WHERE a.id=$1 AND convert_from(j.receipt,'UTF8')::jsonb->>'Duplicate'='false'`, prepared.ID).Scan(&action, &inbox, &phase, &dispatch, &owned, &proofs, &links); err != nil || action != "prepared" || inbox != "running" || phase != "admitted" || !dispatch || !owned || proofs != 0 || links != 0 {
		t.Fatalf("partial failed disposition: %s %s %s %v %v %d %d %v", action, inbox, phase, dispatch, owned, proofs, links, err)
	}
	if _, err := a.db.Exec(`DROP TRIGGER fail_superseded_release ON budget_unused_releases`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ReleaseUnusedPrepared(ctx, prepared.ID); err != nil {
		t.Fatalf("interrupted release did not recover: %v", err)
	}
}

func TestSupersededSendingActionRetainsReservation(t *testing.T) {
	a, prepared, claim, _ := supersededPreparedFixture(t)
	ctx := context.Background()
	// Supersession already won, so an old owner cannot create a sending marker.
	if _, err := a.BeginPrepared(ctx, claim); err == nil {
		t.Fatal("stale owner sent after supersession")
	}
	if _, err := a.ReleaseUnusedPrepared(ctx, prepared.ID); err != nil {
		t.Fatalf("unsent action was not releasable: %v", err)
	}

	// Positive sending evidence from a separate attempt is never rewritten as unused.
	b, intent, actor, command, _ := cancellationFixture(t)
	p, err := b.PrepareAction(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	c, err := b.ClaimPrepared(ctx, p, "sending-owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.BeginPrepared(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, err = b.CancelAttempt(ctx, actor, command); err != nil {
		t.Fatal(err)
	}
	if _, err = b.ReleaseUnusedPrepared(ctx, p.ID); err == nil {
		t.Fatal("sending action refunded")
	}
	var state string
	var marker bool
	var holds, proofs int
	if err = b.db.QueryRow(`SELECT a.state,l.sending_fence IS NOT NULL,(SELECT count(*) FROM budget_holds WHERE action_id=a.id),(SELECT count(*) FROM budget_unused_releases WHERE action_id=a.id) FROM budget_actions a JOIN budget_action_leases l ON l.action_id=a.id WHERE a.id=$1`, p.ID).Scan(&state, &marker, &holds, &proofs); err != nil || state != "sending" || !marker || holds != 3 || proofs != 0 {
		t.Fatalf("sending evidence changed: %s %v %d %d %v", state, marker, holds, proofs, err)
	}
}
