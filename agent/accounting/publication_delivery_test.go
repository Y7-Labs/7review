package accounting

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
)

func publicationOperationFixture(t *testing.T) (*Authority, review.AuthorizePublicationCommand, PublicationOperation, string) {
	t.Helper()
	a, _, actor, _, command, dsn := publicationAuthorizationFixture(t)
	ctx := context.Background()
	if _, err := a.AuthorizePublication(ctx, actor, command); err != nil {
		t.Fatal(err)
	}
	if n, err := a.MaterializeAuthorizedPublications(ctx, 8); err != nil || n != 1 {
		t.Fatalf("materialize: %d %v", n, err)
	}
	op, err := a.LookupPublicationOperation(ctx, command.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	return a, command, op, dsn
}

func TestPublicationBeginObsoletesClaimSupersededBeforeSend(t *testing.T) {
	a, command, _, _ := publicationOperationFixture(t)
	ctx := context.Background()
	claim, err := a.ClaimPublication(ctx, "claimed-before-supersession", time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("claim: %#v %v", claim, err)
	}
	var intentAttempt string
	if err = a.db.QueryRow(`SELECT attempt_id FROM budget_jobs WHERE run_id=$1`, claim.Operation.RunID).Scan(&intentAttempt); err != nil || intentAttempt != claim.Operation.AttemptID {
		t.Fatal(err)
	}
	// Reuse the exact accepted attempt identity retained by the terminal artifact.
	var bindingRaw []byte
	if err = a.db.QueryRow(`SELECT binding FROM budget_attempts WHERE id=$1`, claim.Operation.AttemptID).Scan(&bindingRaw); err != nil {
		t.Fatal(err)
	}
	var binding attemptBinding
	if err = json.Unmarshal(bindingRaw, &binding); err != nil {
		t.Fatal(err)
	}
	supersedePublicationAttempt(t, a, orchestrator.DispatchIntent{Attempt: binding.Attempt})
	if permit, err := a.BeginPublication(ctx, *claim, time.Minute); !errors.Is(err, ErrPublicationObsolete) || permit != nil {
		t.Fatalf("superseded claim began sending: %#v %v", permit, err)
	}
	op, err := a.LookupPublicationOperation(ctx, command.CommandID)
	if err != nil || op.State != review.DeliveryObsolete || op.WorkerID != "" || !op.SendingAt.IsZero() {
		t.Fatalf("superseded claim retained send authority: %#v %v", op, err)
	}
}

func TestPublicationBeginCommitFailureLeavesNoSendingMarker(t *testing.T) {
	a, _, op, _ := publicationOperationFixture(t)
	ctx := context.Background()
	claim, err := a.ClaimPublication(ctx, "begin-owner", time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("claim: %#v %v", claim, err)
	}
	if _, err = a.db.Exec(`CREATE FUNCTION fail_publication_begin() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='sending' THEN RAISE EXCEPTION 'injected publication begin failure'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER fail_publication_begin AFTER UPDATE ON budget_publication_operations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_publication_begin()`); err != nil {
		t.Fatal(err)
	}
	if permit, err := a.BeginPublication(ctx, *claim, time.Minute); err == nil || permit != nil {
		t.Fatalf("failed sending marker acknowledged: %#v %v", permit, err)
	}
	var state string
	var sending bool
	if err = a.db.QueryRow(`SELECT state,sending_at IS NOT NULL FROM budget_publication_operations WHERE id=$1`, op.ID).Scan(&state, &sending); err != nil || state != "pending" || sending {
		t.Fatalf("failed begin partially committed: %s %v %v", state, sending, err)
	}
}

func TestPublicationClaimCommitFailureLeavesNoOwnerOrFence(t *testing.T) {
	a, _, op, _ := publicationOperationFixture(t)
	ctx := context.Background()
	if _, err := a.db.Exec(`CREATE FUNCTION fail_publication_claim() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='pending' AND NEW.worker_id IS NOT NULL THEN RAISE EXCEPTION 'injected publication claim failure'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER fail_publication_claim AFTER UPDATE ON budget_publication_operations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_publication_claim()`); err != nil {
		t.Fatal(err)
	}
	if claim, err := a.ClaimPublication(ctx, "interrupted-worker", time.Minute); err == nil || claim != nil {
		t.Fatalf("failed claim acknowledged: %#v %v", claim, err)
	}
	var worker *string
	var lease *time.Time
	var fence uint64
	if err := a.db.QueryRow(`SELECT worker_id,fence,lease_until FROM budget_publication_operations WHERE id=$1`, op.ID).Scan(&worker, &fence, &lease); err != nil || worker != nil || fence != 0 || lease != nil {
		t.Fatalf("failed claim partially committed: %v %d %v %v", worker, fence, lease, err)
	}
	if _, err := a.db.Exec(`DROP TRIGGER fail_publication_claim ON budget_publication_operations`); err != nil {
		t.Fatal(err)
	}
	if claim, err := a.ClaimPublication(ctx, "recovery-worker", time.Minute); err != nil || claim == nil || claim.Fence != 1 {
		t.Fatalf("interrupted claim did not recover cleanly: %#v %v", claim, err)
	}
}

func TestPublicationClaimIsExclusiveAndExpiredClaimUsesHigherFence(t *testing.T) {
	a, _, op, _ := publicationOperationFixture(t)
	ctx := context.Background()
	first, err := a.ClaimPublication(ctx, "worker-one", time.Minute)
	if err != nil || first == nil || first.Operation.ID != op.ID || first.Fence == 0 {
		t.Fatalf("first claim: %#v %v", first, err)
	}
	if duplicate, err := a.ClaimPublication(ctx, "worker-two", time.Minute); err != nil || duplicate != nil {
		t.Fatalf("duplicate worker claimed live lease: %#v %v", duplicate, err)
	}
	if _, err = a.db.Exec(`UPDATE budget_publication_operations SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	second, err := a.ClaimPublication(ctx, "worker-two", time.Minute)
	if err != nil || second == nil || second.Fence <= first.Fence {
		t.Fatalf("expired claim was not fenced: first=%#v second=%#v err=%v", first, second, err)
	}
	if permit, err := a.BeginPublication(ctx, *first, time.Minute); err == nil || permit != nil {
		t.Fatalf("stale owner created sending marker: %#v %v", permit, err)
	}
	permit, err := a.BeginPublication(ctx, *second, time.Minute)
	if err != nil || permit == nil || permit.Fence != second.Fence || permit.OperationID != op.ID || permit.PayloadDigest != op.PayloadDigest {
		t.Fatalf("current owner could not begin: %#v %v", permit, err)
	}
	if another, err := a.ClaimPublication(ctx, "worker-three", time.Minute); err != nil || another != nil {
		t.Fatalf("sending operation became claimable: %#v %v", another, err)
	}
}

func TestPublicationSendingRecoverySurvivesRestartAndNeverReclaims(t *testing.T) {
	a, command, op, dsn := publicationOperationFixture(t)
	ctx := context.Background()
	claim, err := a.ClaimPublication(ctx, "sending-owner", time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("claim: %#v %v", claim, err)
	}
	if _, err = a.BeginPublication(ctx, *claim, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(`UPDATE budget_publication_operations SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	restartedDB, err := OpenDatabase(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer restartedDB.Close()
	restarted, err := NewAuthority(restartedDB, a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if claim, err := restarted.ClaimPublication(ctx, "recovery-worker", time.Minute); err != nil || claim != nil {
		t.Fatalf("restart reclaimed sending operation: %#v %v", claim, err)
	}
	const workers = 8
	counts := make(chan int, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, recoverErr := restarted.RecoverUncertainPublications(ctx, 8)
			counts <- n
			errs <- recoverErr
		}()
	}
	wg.Wait()
	close(counts)
	close(errs)
	total := 0
	for n := range counts {
		total += n
	}
	for recoverErr := range errs {
		if recoverErr != nil {
			t.Fatal(recoverErr)
		}
	}
	if total != 1 {
		t.Fatalf("duplicate sending recovery transitions: %d", total)
	}
	loaded, err := restarted.LookupPublicationOperation(ctx, command.CommandID)
	if err != nil || loaded.State != review.DeliveryUncertain {
		t.Fatalf("sending process death was not uncertain: %#v %v", loaded, err)
	}
	if n, err := restarted.RecoverUncertainPublications(ctx, 8); err != nil || n != 0 {
		t.Fatalf("uncertain operation was replayed: %d %v", n, err)
	}
	if claim, err := restarted.ClaimPublication(ctx, "late-worker", time.Minute); err != nil || claim != nil {
		t.Fatalf("uncertain operation became claimable: %#v %v", claim, err)
	}
}

func TestPublicationCompletionPersistsExactReceiptAndRejectsStaleOwner(t *testing.T) {
	a, command, op, _ := publicationOperationFixture(t)
	ctx := context.Background()
	claim, err := a.ClaimPublication(ctx, "delivery-owner", time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("claim: %#v %v", claim, err)
	}
	permit, err := a.BeginPublication(ctx, *claim, time.Minute)
	if err != nil || permit == nil {
		t.Fatalf("begin: %#v %v", permit, err)
	}
	receipt := PublicationDeliveryReceipt{Provider: op.Destination.Provider, RemoteID: "comment-42", RemoteURL: "https://provider.test/comment/42", PayloadDigest: op.PayloadDigest}
	stale := *permit
	stale.Fence++
	if completed, err := a.CompletePublication(ctx, stale, receipt); err == nil || completed != nil {
		t.Fatalf("stale permit completed publication: %#v %v", completed, err)
	}
	completed, err := a.CompletePublication(ctx, *permit, receipt)
	if err != nil || completed == nil || completed.State != review.DeliveryDelivered || completed.DeliveryReceipt != receipt || completed.CompletedAt.IsZero() {
		t.Fatalf("exact completion was not persisted: %#v %v", completed, err)
	}
	replayed, err := a.CompletePublication(ctx, *permit, receipt)
	if err != nil || replayed == nil || replayed.DeliveryReceipt != receipt || replayed.CompletedAt != completed.CompletedAt {
		t.Fatalf("exact completion did not replay: %#v %v", replayed, err)
	}
	changed := receipt
	changed.RemoteID = "comment-99"
	if replay, err := a.CompletePublication(ctx, *permit, changed); err == nil || replay != nil {
		t.Fatalf("changed completion receipt replayed: %#v %v", replay, err)
	}
	loaded, err := a.LookupPublicationOperation(ctx, command.CommandID)
	if err != nil || loaded.DeliveryReceipt != receipt || loaded.State != review.DeliveryDelivered {
		t.Fatalf("durable receipt lookup: %#v %v", loaded, err)
	}
}

func TestPublicationCompletionCommitFailureRemainsUncertainNotDelivered(t *testing.T) {
	a, _, op, _ := publicationOperationFixture(t)
	ctx := context.Background()
	claim, err := a.ClaimPublication(ctx, "interrupted-delivery-owner", time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("claim: %#v %v", claim, err)
	}
	permit, err := a.BeginPublication(ctx, *claim, time.Millisecond)
	if err != nil || permit == nil {
		t.Fatalf("begin: %#v %v", permit, err)
	}
	if _, err = a.db.Exec(`CREATE FUNCTION fail_publication_completion() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='delivered' THEN RAISE EXCEPTION 'injected publication completion failure'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER fail_publication_completion AFTER UPDATE ON budget_publication_operations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_publication_completion()`); err != nil {
		t.Fatal(err)
	}
	receipt := PublicationDeliveryReceipt{Provider: op.Destination.Provider, RemoteID: "comment-42", PayloadDigest: op.PayloadDigest}
	if completed, err := a.CompletePublication(ctx, *permit, receipt); err == nil || completed != nil {
		t.Fatalf("failed completion acknowledged: %#v %v", completed, err)
	}
	var state string
	var hasReceipt, hasCompletion bool
	if err = a.db.QueryRow(`SELECT state,delivery_receipt IS NOT NULL,completed_at IS NOT NULL FROM budget_publication_operations WHERE id=$1`, op.ID).Scan(&state, &hasReceipt, &hasCompletion); err != nil || state != "sending" || hasReceipt || hasCompletion {
		t.Fatalf("failed completion partially committed: %s %v %v %v", state, hasReceipt, hasCompletion, err)
	}
	if _, err = a.db.Exec(`DROP TRIGGER fail_publication_completion ON budget_publication_operations`); err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(`UPDATE budget_publication_operations SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := a.RecoverUncertainPublications(ctx, 8); err != nil || n != 1 {
		t.Fatalf("interrupted provider success was not quarantined: %d %v", n, err)
	}
	loaded, err := a.LookupPublicationOperation(ctx, permit.CommandID)
	if err != nil || loaded.State != review.DeliveryUncertain || loaded.DeliveryReceipt != (PublicationDeliveryReceipt{}) {
		t.Fatalf("interrupted completion was not uncertain: %#v %v", loaded, err)
	}
}
