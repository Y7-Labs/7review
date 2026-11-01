package accounting

import (
	"context"
	"github.com/Y4NN777/7review/agent/review"
	"testing"
	"time"
)

// R14/S15: admission must participate in the acceptance transaction, including
// its current-generation pointer, rather than committing behind its caller.
func TestPostgresAdmissionCallerTransactionRollsBackWholeIdentity(t *testing.T) {
	db, _ := postgresFixture(t)
	a := ledgerAuthority(t, db, ledgerLimits())
	input := admissionFromIntent(ledgerIntent(t, db, "fixture", "7", ledgerLimits()))
	tx, err := a.begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.admitTx(context.Background(), tx, input); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"budget_attempts", "budget_heads", "budget_intakes"} {
		var count int
		if err = db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s escaped caller rollback: %d %v", table, count, err)
		}
	}
	if _, err = a.Admit(context.Background(), input); err != nil {
		t.Fatalf("rolled-back generation consumed admission: %v", err)
	}
}

func TestPostgresInboxCallerTransactionCannotLeaveAcceptanceOrphan(t *testing.T) {
	db, _ := postgresFixture(t)
	a := ledgerAuthority(t, db, ledgerLimits())
	tx, err := a.begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.acceptInboxTx(context.Background(), tx, review.Request{Provider: "github", ProjectID: "org/repo", ChangeID: "7"}, time.Minute); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"budget_inbox", "budget_jobs"} {
		var count int
		if err = db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s escaped transaction: %d %v", table, count, err)
		}
	}
}

func preparedAggregate(t *testing.T, a *Authority) (review.Request, review.Admission, review.Source) {
	t.Helper()
	input := admissionFromIntent(ledgerIntent(t, a.db, "fixture", "7", ledgerLimits()))
	input.Policy.TriggerAccepted = true
	input.Request.DeliveryID = input.DeliveryID
	scm := &review.SCMContext{Provider: "github", ProjectID: "org/repo", ChangeID: "7", DiffRefs: review.DiffRefs{BaseSHA: "base", HeadSHA: "head"}, Files: []review.ChangedFile{{NewPath: "service.go", Patch: "+change", Status: "modified"}}}
	_, att, err := review.AttestSCMRequest(input.Request, scm, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	input.Attestation = att
	input.Execution.ComparisonTree = att.ComparisonTree
	source := review.NewContext(input.Request).Source
	source.SCM = scm
	source.Attestation = &att
	source.ChangedFiles = scm.Files
	source.Policy = input.Policy
	return input.Request, input, source
}

func TestPostgresAggregateReplayCannotRebindOrConcealCorruption(t *testing.T) {
	db, _ := postgresFixture(t)
	a := ledgerAuthority(t, db, ledgerLimits())
	req, input, source := preparedAggregate(t, a)
	accepted, err := a.AcceptAggregate(context.Background(), req, input, source, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Title = "conflicting raw input"
	if _, err = a.ReplayAggregate(context.Background(), req); err != ErrInboxConflict {
		t.Fatalf("conflict not rejected before reacquisition: %v", err)
	}
	req.Title = ""
	if _, err = db.Exec(`UPDATE budget_aggregates SET checkpoint=convert_to('corrupt','UTF8') WHERE run_id=$1`, accepted.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err = a.ReplayAggregate(context.Background(), req); err == nil {
		t.Fatal("corrupt checkpoint acknowledged as accepted")
	}
}

// R14/S15: the receipt may escape only after every member of the accepted
// aggregate, including immutable prepared state and its event, commits.
func TestPostgresBefore202AggregateCommitIsAllOrNothing(t *testing.T) {
	db, _ := postgresFixture(t)
	a := ledgerAuthority(t, db, ledgerLimits())
	req, input, source := preparedAggregate(t, a)
	_, err := db.Exec(`CREATE FUNCTION fail_aggregate_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected aggregate commit failure'; END $$; CREATE CONSTRAINT TRIGGER fail_aggregate_commit AFTER INSERT ON budget_aggregates DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_aggregate_commit()`)
	if err != nil {
		t.Fatal(err)
	}
	if item, err := a.AcceptAggregate(context.Background(), req, input, source, []byte("continuation")); err == nil || item != nil {
		t.Fatalf("uncommitted aggregate acknowledged: %#v %v", item, err)
	}
	for _, table := range []string{"budget_inbox", "budget_jobs", "budget_attempts", "budget_heads", "budget_intakes", "budget_aggregates"} {
		var count int
		if err = db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s escaped aggregate rollback: %d %v", table, count, err)
		}
	}
	if _, err = db.Exec(`DROP TRIGGER fail_aggregate_commit ON budget_aggregates`); err != nil {
		t.Fatal(err)
	}
	accepted, err := a.AcceptAggregate(context.Background(), req, input, source, []byte("continuation"))
	if err != nil {
		t.Fatal(err)
	}
	if accepted.AttemptID == "" || accepted.State != "pending" || accepted.JobPhase != "admitted" {
		t.Fatalf("receipt lacks complete identity: %#v", accepted)
	}
	var eventType string
	if err = db.QueryRow(`SELECT initial_event->>'type' FROM budget_aggregates WHERE run_id=$1`, accepted.RunID).Scan(&eventType); err != nil || eventType != "aggregate_accepted" {
		t.Fatalf("accepted event not durable: %s %v", eventType, err)
	}
	claim, err := a.ClaimInbox(context.Background())
	if err != nil || claim == nil {
		t.Fatalf("accepted aggregate not claimable: %#v %v", claim, err)
	}
	checkpoint, err := a.LoadAggregate(context.Background(), review.JobClaim{RunID: claim.RunID, Token: claim.ClaimToken})
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.Source.Attempt.AttemptID != accepted.AttemptID || checkpoint.Source.Run.ID != accepted.RunID || string(checkpoint.Continuation) != "continuation" || checkpoint.Source.SCM.Files[0].Patch != "+change" {
		t.Fatalf("restart lost prepared aggregate: %#v", checkpoint)
	}
	replay, err := a.ReplayAggregate(context.Background(), req)
	if err != nil || replay.RunID != accepted.RunID || replay.AttemptID != accepted.AttemptID || !replay.Deadline.Equal(accepted.Deadline) {
		t.Fatalf("lost-response replay changed aggregate: %#v %v", replay, err)
	}
}
