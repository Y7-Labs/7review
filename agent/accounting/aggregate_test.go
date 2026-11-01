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
