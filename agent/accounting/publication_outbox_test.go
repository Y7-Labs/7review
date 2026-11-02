package accounting

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
)

func TestAuthorizedPublicationMaterializationSurvivesRestartAndDuplicateWorkers(t *testing.T) {
	a, intent, actor, _, command, dsn := publicationAuthorizationFixture(t)
	ctx := context.Background()
	receipt, err := a.AuthorizePublication(ctx, actor, command)
	if err != nil {
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
	const workers = 8
	counts := make(chan int, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, materializeErr := restarted.MaterializeAuthorizedPublications(ctx, 8)
			counts <- n
			errs <- materializeErr
		}()
	}
	wg.Wait()
	close(counts)
	close(errs)
	total := 0
	for n := range counts {
		total += n
	}
	for materializeErr := range errs {
		if materializeErr != nil {
			t.Fatal(materializeErr)
		}
	}
	if total != 1 {
		t.Fatalf("duplicate materializers created or claimed intent: %d", total)
	}
	op, err := restarted.LookupPublicationOperation(ctx, receipt.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := restarted.LookupModelJob(ctx, intent.Job.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if op.AuthorizationCommandID != receipt.CommandID || op.RunID != intent.Job.RunID || op.AttemptID != command.AttemptID || op.AssessmentDigest != command.Payload.AssessmentDigest || op.ArtifactDigest != command.Payload.ArtifactDigest || op.PayloadDigest != command.Payload.ArtifactDigest || op.Destination != command.Payload.Destination || op.State != "pending" || string(op.Payload) != projection.Source.Report.Draft {
		t.Fatalf("materialized operation is not the exact authorized artifact: %#v", op)
	}
	if n, err := restarted.MaterializeAuthorizedPublications(ctx, 8); err != nil || n != 0 {
		t.Fatalf("stable restart replay: %d %v", n, err)
	}
	loaded, err := restarted.LookupPublicationOperation(ctx, receipt.CommandID)
	if err != nil || !reflect.DeepEqual(op, loaded) {
		t.Fatalf("operation changed on replay: %#v %v", loaded, err)
	}
}

func TestSupersededAuthorizationMaterializesObsoleteNeverPending(t *testing.T) {
	a, intent, actor, _, command, _ := publicationAuthorizationFixture(t)
	ctx := context.Background()
	receipt, err := a.AuthorizePublication(ctx, actor, command)
	if err != nil {
		t.Fatal(err)
	}
	supersedePublicationAttempt(t, a, intent)
	if n, err := a.MaterializeAuthorizedPublications(ctx, 8); err != nil || n != 1 {
		t.Fatalf("obsolete materialization: %d %v", n, err)
	}
	op, err := a.LookupPublicationOperation(ctx, receipt.CommandID)
	if err != nil || op.State != review.DeliveryObsolete || op.AttemptID != intent.Attempt.AttemptID || op.ArtifactDigest != command.Payload.ArtifactDigest {
		t.Fatalf("superseded authorization gained delivery authority: %#v %v", op, err)
	}
}

func supersedePublicationAttempt(t *testing.T, a *Authority, intent orchestrator.DispatchIntent) {
	t.Helper()
	ctx := context.Background()
	req, admission, source := preparedAggregate(t, a)
	req.DeliveryID = "publication-supersession"
	req.SourceSHA = "newer-head"
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
}

func TestLaterAuthorizationMaterializationObsoletesEarlierPendingOperation(t *testing.T) {
	a, _, actor, _, first, _ := publicationAuthorizationFixture(t)
	ctx := context.Background()
	if _, err := a.AuthorizePublication(ctx, actor, first); err != nil {
		t.Fatal(err)
	}
	if n, err := a.MaterializeAuthorizedPublications(ctx, 8); err != nil || n != 1 {
		t.Fatalf("first materialization: %d %v", n, err)
	}
	later := first
	later.CommandID = "publish-later"
	later.ExpectedVersion++
	later.Payload.Reason = "new exact disclosure decision"
	if _, err := a.AuthorizePublication(ctx, actor, later); err != nil {
		t.Fatal(err)
	}
	if n, err := a.MaterializeAuthorizedPublications(ctx, 8); err != nil || n != 1 {
		t.Fatalf("later materialization: %d %v", n, err)
	}
	oldOperation, err := a.LookupPublicationOperation(ctx, first.CommandID)
	if err != nil || oldOperation.State != review.DeliveryObsolete {
		t.Fatalf("superseded pending operation remained sendable: %#v %v", oldOperation, err)
	}
	newOperation, err := a.LookupPublicationOperation(ctx, later.CommandID)
	if err != nil || newOperation.State != review.DeliveryPending {
		t.Fatalf("current operation is not pending: %#v %v", newOperation, err)
	}
}

func TestMaterializationSweepObsoletesPendingOperationAfterAttemptSupersession(t *testing.T) {
	a, intent, actor, _, command, _ := publicationAuthorizationFixture(t)
	ctx := context.Background()
	if _, err := a.AuthorizePublication(ctx, actor, command); err != nil {
		t.Fatal(err)
	}
	if n, err := a.MaterializeAuthorizedPublications(ctx, 8); err != nil || n != 1 {
		t.Fatalf("initial materialization: %d %v", n, err)
	}
	supersedePublicationAttempt(t, a, intent)
	if n, err := a.MaterializeAuthorizedPublications(ctx, 8); err != nil || n != 0 {
		t.Fatalf("stale sweep invented operation: %d %v", n, err)
	}
	op, err := a.LookupPublicationOperation(ctx, command.CommandID)
	if err != nil || op.State != review.DeliveryObsolete {
		t.Fatalf("superseded pending operation remained sendable: %#v %v", op, err)
	}
}

func TestPublicationMaterializationFailsClosedOnCorruptStaleOperation(t *testing.T) {
	a, _, actor, _, first, _ := publicationAuthorizationFixture(t)
	ctx := context.Background()
	if _, err := a.AuthorizePublication(ctx, actor, first); err != nil {
		t.Fatal(err)
	}
	if n, err := a.MaterializeAuthorizedPublications(ctx, 8); err != nil || n != 1 {
		t.Fatalf("initial materialization: %d %v", n, err)
	}
	later := first
	later.CommandID = "publish-after-corruption"
	later.ExpectedVersion++
	later.Payload.Reason = "later exact decision"
	if _, err := a.AuthorizePublication(ctx, actor, later); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`UPDATE budget_publication_operations SET payload=convert_to('corrupt','UTF8') WHERE command_id=$1`, first.CommandID); err != nil {
		t.Fatal(err)
	}
	if n, err := a.MaterializeAuthorizedPublications(ctx, 8); !errors.Is(err, ErrIntegrity) || n != 0 {
		t.Fatalf("corrupt stale operation was accepted: %d %v", n, err)
	}
	var oldState string
	var newOperations int
	if err := a.db.QueryRow(`SELECT state,(SELECT count(*) FROM budget_publication_operations WHERE command_id=$1) FROM budget_publication_operations WHERE command_id=$2`, later.CommandID, first.CommandID).Scan(&oldState, &newOperations); err != nil || oldState != "pending" || newOperations != 0 {
		t.Fatalf("integrity failure partially transitioned outbox: %s %d %v", oldState, newOperations, err)
	}
}

func TestPublicationMaterializationCommitFailureLeavesNoOperation(t *testing.T) {
	a, _, actor, _, command, _ := publicationAuthorizationFixture(t)
	if _, err := a.AuthorizePublication(context.Background(), actor, command); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`CREATE FUNCTION fail_publication_outbox() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected publication outbox failure'; END $$; CREATE CONSTRAINT TRIGGER fail_publication_outbox AFTER INSERT ON budget_publication_operations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_publication_outbox()`); err != nil {
		t.Fatal(err)
	}
	if n, err := a.MaterializeAuthorizedPublications(context.Background(), 8); err == nil || n != 0 {
		t.Fatalf("failed materialization acknowledged: %d %v", n, err)
	}
	var operations int
	if err := a.db.QueryRow(`SELECT count(*) FROM budget_publication_operations`).Scan(&operations); err != nil || operations != 0 {
		t.Fatalf("failed materialization escaped rollback: %d %v", operations, err)
	}
}
