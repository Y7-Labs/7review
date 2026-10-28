package accounting

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
)

func admissionFromIntent(i orchestrator.DispatchIntent) review.Admission {
	return review.Admission{Request: review.Request{Provider: i.Attempt.Change.Provider, ProjectID: i.Attempt.Change.RepositoryID, ChangeID: i.Attempt.Change.ChangeNumber, SourceSHA: i.Attempt.Snapshot.HeadRevision, TargetSHA: i.Attempt.Snapshot.BaseRevision}, Change: i.Attempt.Change, Attestation: *i.Attestation, Policy: review.PolicyProjection{Mode: "enforce", Enforced: true, Digest: i.PolicyDigest, Limits: i.PolicyLimits, OperatorCeilings: &i.OperatorCeilings, AllowedCapabilities: []string{"repo.read", "model.review"}}, Execution: i.Execution, IntentKind: review.IntentAutomatic, DeliveryID: "delivery-one"}
}
func dispatchForReceipt(i orchestrator.DispatchIntent, r review.IntakeReceipt) orchestrator.DispatchIntent {
	i.Attempt = r.Attempt
	i.Execution = r.Execution
	i.Attestation = &r.Attestation
	i.PolicyLimits = r.Policy.Limits
	i.PolicyDigest = r.Policy.Digest
	return i
}

func TestPostgresCanonicalIntakeDeduplicatesBeforeGeneratingOrSpending(t *testing.T) {
	db, _ := postgresFixture(t)
	l := ledgerLimits()
	a := ledgerAuthority(t, db, l)
	i := ledgerIntent(t, db, "fixture", "7", l)
	input := admissionFromIntent(i)
	r, err := a.Admit(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if r.Attempt.AttemptID == "" || r.Attempt.WorkIntentID == "" || r.Attempt.Generation != 1 || r.Duplicate {
		t.Fatalf("missing canonical identity: %#v", r)
	}
	input.Execution.DeadlineAt = input.Execution.DeadlineAt.AddDate(0, 0, 1)
	input.Attestation.VerifiedAt = input.Attestation.VerifiedAt.AddDate(0, 0, 1)
	replay, err := a.Admit(context.Background(), input)
	if err != nil || !replay.Duplicate || replay.Attempt.AttemptID != r.Attempt.AttemptID || replay.Execution != r.Execution {
		t.Fatalf("delivery replay changed attempt or deadline: %#v %v", replay, err)
	}
	input.Request.Title = "different payload"
	if _, err := a.Admit(context.Background(), input); err == nil {
		t.Fatal("same delivery ID accepted different normalized payload")
	}
	var conflicts int
	if err = db.QueryRow(`SELECT count(*) FROM budget_intake_conflicts`).Scan(&conflicts); err != nil || conflicts != 1 {
		t.Fatalf("conflicting delivery not audited: %d %v", conflicts, err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM budget_attempts`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate intake allocated attempts: %d %v", count, err)
	}
}

func TestPostgresNewGenerationFencesOldDispatchButRetainsHold(t *testing.T) {
	db, _ := postgresFixture(t)
	l := ledgerLimits()
	l.Change.ModelCalls = 1
	a := ledgerAuthority(t, db, l)
	i := ledgerIntent(t, db, "fixture", "7", l)
	input := admissionFromIntent(i)
	r, err := a.Admit(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	old := dispatchForReceipt(i, r)
	if _, err = a.ReserveAndBegin(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	input.DeliveryID = "delivery-two"
	input.ExpectedGeneration = 1
	input.IntentKind = review.IntentManual
	next, err := a.Admit(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if next.Attempt.Generation != 2 || next.Attempt.PredecessorAttemptID != r.Attempt.AttemptID || next.Attempt.AttemptID == r.Attempt.AttemptID {
		t.Fatalf("rerun overwrote identity: %#v", next)
	}
	old.ActionID = "late-old-worker"
	if _, err = a.ReserveAndBegin(context.Background(), old); err == nil {
		t.Fatal("superseded attempt could dispatch")
	}
	current := dispatchForReceipt(i, next)
	current.ActionID = "new-worker"
	if _, err = a.ReserveAndBegin(context.Background(), current); err == nil {
		t.Fatal("new generation reset the shared change hold")
	}
}

func TestPostgresIntakeCompareAndSwapPreventsLateRollback(t *testing.T) {
	db, _ := postgresFixture(t)
	l := ledgerLimits()
	a := ledgerAuthority(t, db, l)
	i := ledgerIntent(t, db, "fixture", "7", l)
	input := admissionFromIntent(i)
	var wg sync.WaitGroup
	start := make(chan struct{})
	receipts := make(chan review.IntakeReceipt, 2)
	errs := make(chan error, 2)
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			v := input
			if n == 1 {
				v.DeliveryID = "other"
				v.Request.SourceSHA = "new-head"
				v.Attestation.Snapshot.HeadRevision = "new-head"
			}
			r, err := a.Admit(context.Background(), v)
			receipts <- r
			errs <- err
		}(n)
	}
	close(start)
	wg.Wait()
	close(errs)
	successes, stale := 0, 0
	for err := range errs {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrStaleIntake) {
			stale++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || stale != 1 {
		t.Fatalf("late acquisition rolled back current generation: success=%d stale=%d", successes, stale)
	}
}

func TestPostgresEphemeralIntakeDoesNotSupersedeService(t *testing.T) {
	db, _ := postgresFixture(t)
	l := ledgerLimits()
	a := ledgerAuthority(t, db, l)
	i := ledgerIntent(t, db, "fixture", "7", l)
	input := admissionFromIntent(i)
	service, err := a.Admit(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	input.Execution.Mode = review.ExecutionEphemeral
	input.Execution.Persistence = review.PersistenceJobLocal
	input.Execution.PipelineID = "pipeline"
	input.Execution.JobID = "job"
	input.DeliveryID = "ci-job"
	input.IntentKind = review.IntentManual
	ci, err := a.Admit(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if ci.Attempt.AttemptID == service.Attempt.AttemptID || ci.Attempt.Generation != 1 {
		t.Fatal("CI falsely claimed the service generation")
	}
	gen, err := a.ObserveGeneration(context.Background(), input.Change)
	if err != nil || gen != 1 {
		t.Fatalf("CI moved service tracking pointer: %d %v", gen, err)
	}
	if _, err = a.ReserveAndBegin(context.Background(), dispatchForReceipt(i, service)); err != nil {
		t.Fatalf("CI fenced service work: %v", err)
	}
}

func TestPostgresIntakeCheckpointSurvivesReopen(t *testing.T) {
	db, _ := postgresFixture(t)
	l := ledgerLimits()
	a := ledgerAuthority(t, db, l)
	i := ledgerIntent(t, db, "fixture", "7", l)
	input := admissionFromIntent(i)
	r, err := a.Admit(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err = db.QueryRow(`SELECT receipt FROM budget_intakes`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var checkpoint review.IntakeReceipt
	if err = json.Unmarshal(raw, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if checkpoint.Attempt.AttemptID != r.Attempt.AttemptID || checkpoint.Attestation.Snapshot != r.Attestation.Snapshot {
		t.Fatal("canonical checkpoint incomplete")
	}
	if _, err = a.ReserveAndBegin(context.Background(), dispatchForReceipt(i, checkpoint)); err != nil {
		t.Fatalf("checkpoint cannot authorize correctly bound dispatch: %v", err)
	}
}

func TestPostgresAutomaticSameSnapshotDoesNotCreateNewGeneration(t *testing.T) {
	db, _ := postgresFixture(t)
	l := ledgerLimits()
	a := ledgerAuthority(t, db, l)
	i := ledgerIntent(t, db, "fixture", "7", l)
	input := admissionFromIntent(i)
	first, err := a.Admit(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	input.DeliveryID = "another-event"
	input.ExpectedGeneration = 1
	next, err := a.Admit(context.Background(), input)
	if err != nil || !next.Duplicate || next.Attempt.AttemptID != first.Attempt.AttemptID {
		t.Fatalf("same automatic snapshot churned a new generation: %#v %v", next, err)
	}
}
