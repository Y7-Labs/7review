package accounting

import (
	"context"
	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
	"sync"
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

func TestPostgresFailedAggregateSupersessionPreservesCurrentAttemptAndHold(t *testing.T) {
	db, _ := postgresFixture(t)
	a := ledgerAuthority(t, db, ledgerLimits())
	req, input, source := preparedAggregate(t, a)
	first, err := a.AcceptAggregate(context.Background(), req, input, source, nil)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := a.ClaimInbox(context.Background())
	if err != nil || claim == nil {
		t.Fatalf("claim: %#v %v", claim, err)
	}
	old, err := a.LoadAggregate(context.Background(), review.JobClaim{RunID: claim.RunID, Token: claim.ClaimToken})
	if err != nil {
		t.Fatal(err)
	}
	req.DeliveryID = "next-delivery"
	req.SourceSHA = "next-head"
	input.Request = req
	input.DeliveryID = req.DeliveryID
	input.ExpectedGeneration = 1
	scm := *source.SCM
	scm.DiffRefs.HeadSHA = req.SourceSHA
	source.SCM = &scm
	source.Request = req
	_, att, err := review.AttestSCMRequest(req, &scm, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	input.Attestation = att
	input.Execution.ComparisonTree = att.ComparisonTree
	source.Attestation = &att
	_, err = db.Exec(`CREATE FUNCTION fail_superseding_aggregate() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected supersession failure'; END $$; CREATE CONSTRAINT TRIGGER fail_superseding_aggregate AFTER INSERT ON budget_aggregates DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_superseding_aggregate()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.AcceptAggregate(context.Background(), req, input, source, nil); err == nil {
		t.Fatal("failed supersession acknowledged")
	}
	var current string
	var generation int
	if err = db.QueryRow(`SELECT current_attempt_id,generation FROM budget_heads`).Scan(&current, &generation); err != nil || current != first.AttemptID || generation != 1 {
		t.Fatalf("failed acceptance obsoleted old attempt: %s %d %v", current, generation, err)
	}
	i := dispatchForReceipt(ledgerIntent(t, db, "fixture-dispatch", "7", ledgerLimits()), old.Receipt)
	i.Job = &review.JobClaim{RunID: claim.RunID, Token: claim.ClaimToken}
	if _, err = a.ReserveAndBegin(context.Background(), i); err != nil {
		t.Fatalf("rollback fenced still-current work: %v", err)
	}
	if _, err = db.Exec(`DROP TRIGGER fail_superseding_aggregate ON budget_aggregates`); err != nil {
		t.Fatal(err)
	}
	if _, err = a.AcceptAggregate(context.Background(), req, input, source, nil); err != nil {
		t.Fatal(err)
	}
	i.ActionID = "late-old-dispatch"
	if _, err = a.ReserveAndBegin(context.Background(), i); err == nil {
		t.Fatal("committed supersession did not fence old dispatch")
	}
	var holds int
	if err = db.QueryRow(`SELECT count(*) FROM budget_holds`).Scan(&holds); err != nil || holds != 3 {
		t.Fatalf("supersession reset uncertain spend: %d %v", holds, err)
	}
}

func TestPostgresConcurrentBefore202ReplaysShareOneAggregate(t *testing.T) {
	db, _ := postgresFixture(t)
	a := ledgerAuthority(t, db, ledgerLimits())
	req, input, source := preparedAggregate(t, a)
	var wg sync.WaitGroup
	receipts := make(chan *InboxItem, 4)
	for n := 0; n < 4; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			item, err := a.AcceptAggregate(context.Background(), req, input, source, nil)
			if err != nil {
				t.Error(err)
			}
			receipts <- item
		}()
	}
	wg.Wait()
	close(receipts)
	var id string
	for item := range receipts {
		if item == nil {
			t.Fatal("concurrent acceptance failed")
		}
		if id == "" {
			id = item.RunID
		}
		if item.RunID != id || item.AttemptID == "" {
			t.Fatalf("replay allocated another identity: %#v", item)
		}
	}
	for _, table := range []string{"budget_inbox", "budget_jobs", "budget_attempts", "budget_intakes", "budget_aggregates"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s duplicated: %d %v", table, count, err)
		}
	}
}

func TestPostgresV6UpgradeRetainsHoldsAndLateResultsWithoutReauthorizing(t *testing.T) {
	a, i, permit, runID, _ := linkedAction(t)
	// Rehearse a v6 accepted job: no complete immutable aggregate existed.
	if _, err := a.db.Exec(`ALTER TABLE budget_jobs DROP COLUMN model_batch_id; DROP TABLE budget_model_batches; ALTER TABLE budget_jobs DROP COLUMN terminal_reduction_id; DROP TABLE budget_model_continuations,budget_model_reduction_inputs,budget_model_reductions,budget_action_leases,budget_aggregates; ALTER TABLE budget_jobs DROP COLUMN recovery_action_id; ALTER TABLE budget_actions DROP COLUMN fenced; ALTER TABLE budget_actions DROP CONSTRAINT budget_actions_state_check; ALTER TABLE budget_actions ADD CHECK(state IN ('sending','uncertain','result_recorded')); UPDATE budget_schema SET version=6`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), a.db); err != nil {
		t.Fatal(err)
	}
	i.ActionID = "obsolete-owner-new-send"
	if _, err := a.ReserveAndBegin(context.Background(), i); err == nil {
		t.Fatal("migration reauthorized legacy job")
	}
	response := []byte("late attributable response")
	if err := a.RecordOutcome(context.Background(), permit, orchestrator.DispatchOutcome{Response: response, ResponseReceived: true, ResponseDigest: hash(response), ElapsedMS: 1}); err != nil {
		t.Fatal(err)
	}
	if n, err := a.RecoverActionResults(context.Background(), 32); err != nil || n != 1 {
		t.Fatalf("upgrade lost original response ownership: %d %v", n, err)
	}
	status, err := a.LookupInbox(context.Background(), runID)
	if err != nil || status.JobPhase != "legacy_unknown" || status.RecordedUnvalidated != 1 {
		t.Fatalf("legacy recovery fabricated state: %#v %v", status, err)
	}
	var holds int
	if err = a.db.QueryRow(`SELECT count(*) FROM budget_holds`).Scan(&holds); err != nil || holds != 3 {
		t.Fatalf("migration reset unresolved holds: %d %v", holds, err)
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
	expired := input
	expired.Execution.DeadlineAt = time.Now().Add(-time.Minute)
	replay, err := a.AcceptAggregate(context.Background(), req, expired, review.Source{}, nil)
	if err != nil || replay.RunID != accepted.RunID || !replay.Deadline.Equal(accepted.Deadline) {
		t.Fatalf("expired retry failed to resolve committed receipt: %#v %v", replay, err)
	}
	req.Title = "conflicting raw input"
	if _, err = a.ReplayAggregate(context.Background(), req); err != ErrInboxConflict {
		t.Fatalf("conflict not rejected before reacquisition: %v", err)
	}
	req.Title = ""
	if _, err = db.Exec(`UPDATE budget_aggregates SET initial_event=jsonb_set(initial_event,'{run_id}','"wrong-run"') WHERE run_id=$1`, accepted.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err = a.ReplayAggregate(context.Background(), req); err == nil {
		t.Fatal("cross-run acceptance event acknowledged")
	}
	if _, err = db.Exec(`UPDATE budget_aggregates SET initial_event=jsonb_set(initial_event,'{run_id}',to_jsonb($1::text)) WHERE run_id=$1`, accepted.RunID); err != nil {
		t.Fatal(err)
	}
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
