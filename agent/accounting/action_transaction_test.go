package accounting

import (
	"context"
	"testing"
)

func TestPreparedReservationParticipatesInCallerTransaction(t *testing.T) {
	a, i, _ := preparedActionFixture(t)
	ctx := context.Background()
	tx, err := a.begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.reserveActionTx(ctx, tx, i, true)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if p.ID != i.ActionID || p.AlreadyStarted {
		tx.Rollback()
		t.Fatalf("invalid private preparation: %#v", p)
	}
	var held int
	if err = tx.QueryRow(`SELECT count(*) FROM budget_holds h JOIN budget_actions a ON a.id=h.action_id WHERE a.id=$1 AND a.state='prepared'`, i.ActionID).Scan(&held); err != nil || held != 3 {
		tx.Rollback()
		t.Fatalf("private preparation lacks all holds: %d %v", held, err)
	}
	if _, err = tx.Exec(`UPDATE budget_jobs SET recovery_action_id=$1 WHERE run_id=$2`, i.ActionID, i.Job.RunID); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`SELECT count(*) FROM budget_actions`, `SELECT count(*) FROM budget_holds`, `SELECT count(*) FROM budget_action_leases`, `SELECT count(*) FROM budget_jobs WHERE recovery_action_id IS NOT NULL`} {
		var count int
		if err = a.db.QueryRow(query).Scan(&count); err != nil || count != 0 {
			t.Fatalf("reservation escaped caller rollback: count=%d err=%v", count, err)
		}
	}
	if _, err = a.PrepareAction(ctx, i); err != nil {
		t.Fatalf("rolled-back reservation consumed capacity: %v", err)
	}
}
