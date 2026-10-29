package accounting

import (
	"context"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/review"
)

func TestPostgresInboxJobAndAdmissionLinkAreAtomic(t *testing.T) {
	db, _ := postgresFixture(t)
	a := ledgerAuthority(t, db, ledgerLimits())
	input := admissionFromIntent(ledgerIntent(t, db, "fixture", "7", ledgerLimits()))
	item, err := a.AcceptInbox(context.Background(), input.Request, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var jobs int
	if err = db.QueryRow(`SELECT count(*) FROM budget_jobs WHERE run_id=$1 AND attempt_id IS NULL`, item.RunID).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("accepted inbox has no atomic job: %d %v", jobs, err)
	}
	claimed, err := a.ClaimInbox(context.Background())
	if err != nil || claimed == nil {
		t.Fatalf("claim: %#v %v", claimed, err)
	}
	input.Job = &review.JobClaim{RunID: claimed.RunID, Token: claimed.ClaimToken}
	input.Execution.DeadlineAt = claimed.Deadline
	// Failure at final commit must roll back both attempt and job link.
	_, err = db.Exec(`CREATE FUNCTION fail_job_link() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected admission commit failure'; END $$; CREATE CONSTRAINT TRIGGER fail_job_link AFTER UPDATE ON budget_jobs DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_job_link()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Admit(context.Background(), input); err == nil {
		t.Fatal("uncommitted admission confirmed")
	}
	var attempts int
	if err = db.QueryRow(`SELECT count(*) FROM budget_attempts`).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("attempt escaped failed job transaction: %d %v", attempts, err)
	}
	if _, err = db.Exec(`DROP TRIGGER fail_job_link ON budget_jobs`); err != nil {
		t.Fatal(err)
	}
	receipt, err := a.Admit(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	var attemptID string
	if err = db.QueryRow(`SELECT attempt_id FROM budget_jobs WHERE run_id=$1`, item.RunID).Scan(&attemptID); err != nil || attemptID != receipt.Attempt.AttemptID {
		t.Fatalf("job not linked: %s %v", attemptID, err)
	}
	replay, err := a.Admit(context.Background(), input)
	if err != nil || replay.Attempt.AttemptID != attemptID {
		t.Fatalf("admission retry rebound job: %#v %v", replay, err)
	}
	input.Job.Token = "obsolete-owner"
	if _, err = a.Admit(context.Background(), input); err == nil {
		t.Fatal("unowned admission replay accepted")
	}
}

func TestPostgresInboxCannotAcknowledgeWithoutJobAndManualRetryDoesNotRebind(t *testing.T) {
	db, _ := postgresFixture(t)
	a := ledgerAuthority(t, db, ledgerLimits())
	input := admissionFromIntent(ledgerIntent(t, db, "fixture", "7", ledgerLimits()))
	input.DeliveryID = ""
	_, err := db.Exec(`CREATE FUNCTION fail_job_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected job creation failure'; END $$; CREATE CONSTRAINT TRIGGER fail_job_insert AFTER INSERT ON budget_jobs DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_job_insert()`)
	if err != nil {
		t.Fatal(err)
	}
	if item, err := a.AcceptInbox(context.Background(), input.Request, time.Minute); err == nil || item != nil {
		t.Fatalf("orphan inbox acknowledged: %#v %v", item, err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM budget_inbox`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("orphan inbox survived failed acceptance: %d %v", count, err)
	}
	if _, err = db.Exec(`DROP TRIGGER fail_job_insert ON budget_jobs`); err != nil {
		t.Fatal(err)
	}
	if _, err = a.AcceptInbox(context.Background(), input.Request, time.Minute); err != nil {
		t.Fatal(err)
	}
	claimed, err := a.ClaimInbox(context.Background())
	if err != nil || claimed == nil {
		t.Fatalf("claim: %#v %v", claimed, err)
	}
	input.Execution.DeadlineAt = claimed.Deadline
	input.Job = &review.JobClaim{RunID: claimed.RunID, Token: claimed.ClaimToken}
	first, err := a.Admit(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := a.Admit(context.Background(), input)
	if err != nil || replay.Attempt.AttemptID != first.Attempt.AttemptID {
		t.Fatalf("manual job retry allocated different attempt: %#v %v", replay, err)
	}
	input.Request.ProjectID = "different/repository"
	if _, err = a.Admit(context.Background(), input); err == nil {
		t.Fatal("job was rebound to another request")
	}
}
