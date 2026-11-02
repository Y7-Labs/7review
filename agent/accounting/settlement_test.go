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

type testReceiptVerifier struct{}

func (testReceiptVerifier) VerifyFinal(_ context.Context, r FinalReceipt) error {
	if r.EvidenceRef != "authenticated-test-receipt" {
		return errors.New("receipt authentication failed")
	}
	return nil
}
func reconciler(a *Authority) *Authority {
	copy := *a
	copy.cfg.ReceiptVerifier = testReceiptVerifier{}
	return &copy
}
func finalReceipt(i orchestrator.DispatchIntent) FinalReceipt {
	return FinalReceipt{SchemaVersion: 1, ReceiptID: "receipt-" + i.ActionID, ActionID: i.ActionID, RequestDigest: i.RequestDigest, Provider: i.Provider, Model: i.Model, Currency: "USD", EvidenceRef: "authenticated-test-receipt", Certainty: "final", Actual: review.ResourceLimitScope{ModelCalls: 1, InputTokens: 10, OutputTokens: 2, MoneyMicro: 2, ActiveMS: 1}}
}
func admit(t *testing.T, a *Authority, i orchestrator.DispatchIntent) orchestrator.DispatchPermit {
	t.Helper()
	if err := a.RegisterAttempt(context.Background(), i); err != nil {
		t.Fatal(err)
	}
	p, err := a.ReserveAndBegin(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPostgresFinalSettlementReplacesHoldExactlyOnce(t *testing.T) {
	db, _ := postgresFixture(t)
	l := ledgerLimits()
	l.Project.MoneyMicro = 6
	a := ledgerAuthority(t, db, l)
	i := ledgerIntent(t, db, "one", "7", l)
	admit(t, a, i)
	j := ledgerIntent(t, db, "two", "8", l)
	if err := a.RegisterAttempt(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ReserveAndBegin(context.Background(), j); err == nil {
		t.Fatal("unsettled hold was ignored")
	}
	r := finalReceipt(i)
	b := reconciler(a)
	if err := b.SettleFinal(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if err := b.SettleFinal(context.Background(), r); err != nil {
		t.Fatalf("receipt replay changed settlement: %v", err)
	}
	if _, err := a.ReserveAndBegin(context.Background(), j); err != nil {
		t.Fatalf("verified unused allowance not released: %v", err)
	}
	k := ledgerIntent(t, db, "three", "9", l)
	if err := a.RegisterAttempt(context.Background(), k); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ReserveAndBegin(context.Background(), k); err == nil {
		t.Fatal("receipt replay double-released allowance")
	}
	var settled, receipts int
	_ = db.QueryRow(`SELECT count(*) FROM budget_actions WHERE settled`).Scan(&settled)
	_ = db.QueryRow(`SELECT count(*) FROM budget_receipts`).Scan(&receipts)
	if settled != 1 || receipts != 1 {
		t.Fatalf("settlement not unique: settled=%d receipts=%d", settled, receipts)
	}
}

func TestPostgresSettlementKeepsOriginAndNeverRenewsAttempt(t *testing.T) {
	for _, scope := range []string{"change", "project", "attempt"} {
		t.Run(scope, func(t *testing.T) {
			db, _ := postgresFixture(t)
			l := ledgerLimits()
			switch scope {
			case "attempt":
				l.Attempt.ModelCalls = 1
			case "change":
				l.Change.MoneyMicro = 4
			case "project":
				l.Project.MoneyMicro = 4
			}
			a := ledgerAuthority(t, db, l)
			i := ledgerIntent(t, db, "one", "7", l)
			admit(t, a, i)
			if _, err := db.Exec(`UPDATE budget_holds SET origin_period='old-period' WHERE scope_id NOT LIKE 'attempt:%'`); err != nil {
				t.Fatal(err)
			}
			j := i
			j.ActionID = "new-call"
			if scope != "attempt" {
				j = ledgerIntent(t, db, "two", "7", l)
				if err := a.RegisterAttempt(context.Background(), j); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := a.ReserveAndBegin(context.Background(), j); err == nil {
				t.Fatal("old unknown hold disappeared at renewal")
			}
			if err := reconciler(a).SettleFinal(context.Background(), finalReceipt(i)); err != nil {
				t.Fatal(err)
			}
			_, err := a.ReserveAndBegin(context.Background(), j)
			if scope == "attempt" && err == nil {
				t.Fatal("attempt lifetime call allowance renewed")
			}
			if scope != "attempt" && err != nil {
				t.Fatalf("past-period actual charged to current period: %v", err)
			}
			var origin string
			if err = db.QueryRow(`SELECT origin_period FROM budget_holds WHERE action_id=$1 AND scope_id LIKE 'project:%'`, i.ActionID).Scan(&origin); err != nil || origin != "old-period" {
				t.Fatalf("settlement rewrote origin: %s %v", origin, err)
			}
		})
	}
}

func TestPostgresUnverifiedPartialAndMismatchedReceiptsCannotReleaseHolds(t *testing.T) {
	for _, kind := range []string{"no verifier", "unauthenticated", "partial", "wrong request", "wrong provider", "wrong model", "wrong currency", "unknown action", "negative usage"} {
		t.Run(kind, func(t *testing.T) {
			db, _ := postgresFixture(t)
			l := ledgerLimits()
			l.Project.ModelCalls = 1
			a := ledgerAuthority(t, db, l)
			i := ledgerIntent(t, db, "one", "7", l)
			admit(t, a, i)
			b := reconciler(a)
			r := finalReceipt(i)
			switch kind {
			case "no verifier":
				b = a
			case "unauthenticated":
				r.EvidenceRef = "untrusted"
			case "partial":
				r.Certainty = "partial"
			case "wrong request":
				r.RequestDigest = digestBytes([]byte("different"))
			case "wrong provider":
				r.Provider = "other"
			case "wrong model":
				r.Model = "alias"
			case "wrong currency":
				r.Currency = "EUR"
			case "unknown action":
				r.ActionID = "missing"
			case "negative usage":
				r.Actual.InputTokens = -1
			}
			if err := b.SettleFinal(context.Background(), r); err == nil {
				t.Fatal("invalid receipt released hold")
			}
			var settled bool
			if err := db.QueryRow(`SELECT settled FROM budget_actions WHERE id=$1`, i.ActionID).Scan(&settled); err != nil || settled {
				t.Fatalf("hold changed after invalid receipt: %v %v", settled, err)
			}
			if kind == "wrong request" || kind == "wrong provider" || kind == "wrong model" || kind == "wrong currency" || kind == "unknown action" {
				var count int
				if err := db.QueryRow(`SELECT count(*) FROM budget_quarantine`).Scan(&count); err != nil || count != 1 {
					t.Fatalf("unmatched receipt not quarantined: %d %v", count, err)
				}
			}
		})
	}
}

func TestPostgresOverBoundUsageIsRecordedAndBlocksFurtherSpend(t *testing.T) {
	for _, resource := range []string{"money", "input", "output", "active"} {
		t.Run(resource, func(t *testing.T) {
			db, _ := postgresFixture(t)
			l := ledgerLimits()
			a := ledgerAuthority(t, db, l)
			i := ledgerIntent(t, db, "one", "7", l)
			admit(t, a, i)
			r := finalReceipt(i)
			switch resource {
			case "money":
				r.Actual.MoneyMicro = 5
			case "input":
				r.Actual.InputTokens = 101
			case "output":
				r.Actual.OutputTokens = 21
			case "active":
				r.Actual.ActiveMS = 51
			}
			b := reconciler(a)
			if err := b.SettleFinal(context.Background(), r); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("over-bound actual not treated as integrity incident: %v", err)
			}
			if err := b.SettleFinal(context.Background(), r); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("replay erased integrity incident: %v", err)
			}
			var raw []byte
			if err := db.QueryRow(`SELECT actual_amount FROM budget_actions WHERE id=$1`, i.ActionID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var actual review.ResourceLimitScope
			_ = json.Unmarshal(raw, &actual)
			if actual.MoneyMicro != r.Actual.MoneyMicro || actual.InputTokens != r.Actual.InputTokens {
				t.Fatal("over-bound actual replaced with reservation")
			}
			if _, err := db.Exec(`UPDATE budget_holds SET origin_period='old-period' WHERE scope_id NOT LIKE 'attempt:%'`); err != nil {
				t.Fatal(err)
			}
			j := ledgerIntent(t, db, "two", "8", l)
			if err := a.RegisterAttempt(context.Background(), j); err != nil {
				t.Fatal(err)
			}
			if _, err := a.ReserveAndBegin(context.Background(), j); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("project spending continued after integrity incident: %v", err)
			}
		})
	}
}

func TestPostgresKnownActiveOverrunBlocksEvenWithoutTokenReceipt(t *testing.T) {
	db, _ := postgresFixture(t)
	l := ledgerLimits()
	a := ledgerAuthority(t, db, l)
	i := ledgerIntent(t, db, "one", "7", l)
	p := admit(t, a, i)
	o := orchestrator.DispatchOutcome{ResponseDigest: digestBytes(nil), ElapsedMS: 51}
	if err := a.RecordOutcome(context.Background(), p, o); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("known active overrun ignored: %v", err)
	}
	j := ledgerIntent(t, db, "two", "8", l)
	if err := a.RegisterAttempt(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ReserveAndBegin(context.Background(), j); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("unknown token usage concealed known overrun: %v", err)
	}
}

func TestPostgresReceiptIdentityCannotBeReusedOrChanged(t *testing.T) {
	db, _ := postgresFixture(t)
	l := ledgerLimits()
	a := ledgerAuthority(t, db, l)
	i := ledgerIntent(t, db, "one", "7", l)
	admit(t, a, i)
	r := finalReceipt(i)
	b := reconciler(a)
	if err := b.SettleFinal(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	r.Actual.MoneyMicro = 1
	if err := b.SettleFinal(context.Background(), r); err == nil {
		t.Fatal("same receipt repriced historical usage")
	}
	j := ledgerIntent(t, db, "two", "8", l)
	admit(t, a, j)
	other := finalReceipt(j)
	other.ReceiptID = r.ReceiptID
	if err := b.SettleFinal(context.Background(), other); err == nil {
		t.Fatal("same receipt settled another action")
	}
	var settled bool
	_ = db.QueryRow(`SELECT settled FROM budget_actions WHERE id=$1`, j.ActionID).Scan(&settled)
	if settled {
		t.Fatal("conflicting receipt changed another hold")
	}
}

func TestPostgresConcurrentFinalReceiptReplaysSettleOnlyOnce(t *testing.T) {
	db, _ := postgresFixture(t)
	l := ledgerLimits()
	a := ledgerAuthority(t, db, l)
	i := ledgerIntent(t, db, "one", "7", l)
	admit(t, a, i)
	b := reconciler(a)
	r := finalReceipt(i)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- b.SettleFinal(context.Background(), r) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM budget_receipts`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("concurrent receipt replay duplicated usage: %d %v", count, err)
	}
}

func TestPostgresSettlementRollbackDoesNotReleaseOrLoseReceipt(t *testing.T) {
	db, _ := postgresFixture(t)
	l := ledgerLimits()
	a := ledgerAuthority(t, db, l)
	i := ledgerIntent(t, db, "one", "7", l)
	admit(t, a, i)
	b := reconciler(a)
	r := finalReceipt(i)
	if _, err := db.Exec(`ALTER TABLE budget_actions ADD CONSTRAINT settlement_fault CHECK (NOT settled)`); err != nil {
		t.Fatal(err)
	}
	if err := b.SettleFinal(context.Background(), r); err == nil {
		t.Fatal("failed settlement returned success")
	}
	var count int
	var settled bool
	if err := db.QueryRow(`SELECT count(*) FROM budget_receipts`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed settlement consumed receipt: %d %v", count, err)
	}
	if err := db.QueryRow(`SELECT settled FROM budget_actions WHERE id=$1`, i.ActionID).Scan(&settled); err != nil || settled {
		t.Fatal("failed settlement released hold")
	}
	if _, err := db.Exec(`ALTER TABLE budget_actions DROP CONSTRAINT settlement_fault`); err != nil {
		t.Fatal(err)
	}
	if err := b.SettleFinal(context.Background(), r); err != nil {
		t.Fatalf("settlement could not recover after SQL rollback: %v", err)
	}
}

func TestPostgresLateSettlementCannotReviveClosedAttempt(t *testing.T) {
	db, _ := postgresFixture(t)
	l := ledgerLimits()
	a := ledgerAuthority(t, db, l)
	i := ledgerIntent(t, db, "one", "7", l)
	admit(t, a, i)
	if err := a.CloseAttempt(context.Background(), i.Attempt.AttemptID); err != nil {
		t.Fatal(err)
	}
	if err := reconciler(a).SettleFinal(context.Background(), finalReceipt(i)); err != nil {
		t.Fatal(err)
	}
	i.ActionID = "late-worker-new-call"
	if _, err := a.ReserveAndBegin(context.Background(), i); err == nil {
		t.Fatal("late receipt revived old worker")
	}
}

func TestPostgresV1MigrationPreservesExistingSendingActions(t *testing.T) {
	db, _ := postgresFixture(t)
	l := ledgerLimits()
	a := ledgerAuthority(t, db, l)
	i := ledgerIntent(t, db, "one", "7", l)
	admit(t, a, i)
	if _, err := db.Exec(`ALTER TABLE budget_jobs DROP COLUMN model_batch_id; DROP TABLE budget_model_batches; ALTER TABLE budget_jobs DROP COLUMN terminal_reduction_id; DROP TABLE budget_model_continuations,budget_model_reduction_inputs,budget_model_reductions,budget_action_leases,budget_aggregates,budget_action_results,budget_jobs,budget_inbox_conflicts,budget_inbox,budget_receipts,budget_quarantine; DROP INDEX budget_actions_attempt; ALTER TABLE budget_actions DROP COLUMN integrity_incident; ALTER TABLE budget_actions DROP COLUMN fenced; ALTER TABLE budget_actions DROP CONSTRAINT budget_actions_state_check; ALTER TABLE budget_actions ADD CHECK(state IN ('sending','uncertain','result_recorded')); UPDATE budget_schema SET version=1`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	p, err := a.ReserveAndBegin(context.Background(), i)
	if err != nil || !p.AlreadyStarted {
		t.Fatalf("migration reset sending state: %#v %v", p, err)
	}
	if err := reconciler(a).SettleFinal(context.Background(), finalReceipt(i)); err != nil {
		t.Fatalf("migrated hold cannot settle: %v", err)
	}
}

func TestPostgresMigrationCannotReplaceMissingLedgerWithEmptyStore(t *testing.T) {
	db, _ := postgresFixture(t)
	l := ledgerLimits()
	a := ledgerAuthority(t, db, l)
	i := ledgerIntent(t, db, "one", "7", l)
	admit(t, a, i)
	if _, err := db.Exec(`DROP TABLE budget_actions CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), db); err == nil {
		t.Fatal("damaged ledger silently recreated as empty accounting")
	}
}

func TestPostgresConcurrentConflictingReceiptIdentityIsQuarantined(t *testing.T) {
	db, _ := postgresFixture(t)
	l := ledgerLimits()
	a := ledgerAuthority(t, db, l)
	i := ledgerIntent(t, db, "one", "7", l)
	admit(t, a, i)
	cfg := a.cfg
	cfg.ProjectID = "other-project"
	b, err := NewAuthority(db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	j := ledgerIntent(t, db, "two", "8", l)
	admit(t, b, j)
	if _, err = db.Exec(`CREATE FUNCTION delay_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(0.15); RETURN NEW; END $$; CREATE TRIGGER delay_receipt_insert BEFORE INSERT ON budget_receipts FOR EACH ROW EXECUTE FUNCTION delay_receipt()`); err != nil {
		t.Fatal(err)
	}
	r1, r2 := finalReceipt(i), finalReceipt(j)
	r2.ReceiptID = r1.ReceiptID
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for n, r := range []FinalReceipt{r1, r2} {
		wg.Add(1)
		go func(n int, r FinalReceipt) {
			defer wg.Done()
			<-start
			authority := a
			if n == 1 {
				authority = b
			}
			errs <- reconciler(authority).SettleFinal(context.Background(), r)
		}(n, r)
	}
	close(start)
	wg.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		}
	}
	var quarantined int
	if err = db.QueryRow(`SELECT count(*) FROM budget_quarantine`).Scan(&quarantined); err != nil {
		t.Fatal(err)
	}
	if successes != 1 || quarantined != 1 {
		t.Fatalf("racing receipt conflict lost audit: settled=%d quarantined=%d", successes, quarantined)
	}
}
