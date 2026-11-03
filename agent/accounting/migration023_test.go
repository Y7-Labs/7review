package accounting

import (
	"context"
	"testing"
	"time"
)

func TestV23MigrationIsExplicitAtomicAndSchedulesExistingUncertainPublication(t *testing.T) {
	a, _, op, _ := publicationOperationFixture(t)
	ctx := context.Background()
	claim, err := a.ClaimPublication(ctx, "v23-sending-owner", time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("claim: %#v %v", claim, err)
	}
	if _, err = a.BeginPublication(ctx, *claim, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(`UPDATE budget_publication_operations SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := a.RecoverUncertainPublications(ctx, 8); err != nil || n != 1 {
		t.Fatalf("recover uncertain: %d %v", n, err)
	}
	var before string
	if err = a.db.QueryRow(`SELECT (to_jsonb(o)-'reconcile_after')::text FROM budget_publication_operations o WHERE id=$1`, op.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(`ALTER TABLE budget_publication_operations DROP CONSTRAINT budget_publication_operation_reconciliation,DROP COLUMN reconcile_after; UPDATE budget_schema SET version=22; CREATE FUNCTION fail_v23_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=23 THEN RAISE EXCEPTION 'injected v23 commit failure'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER fail_v23_commit AFTER UPDATE ON budget_schema DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_v23_commit()`); err != nil {
		t.Fatal(err)
	}
	if tx, err := a.begin(ctx); err == nil {
		tx.Rollback()
		t.Fatal("runtime accepted unmigrated v22")
	}
	if err = Migrate(ctx, a.db); err == nil {
		t.Fatal("failed v23 migration reported success")
	}
	var version, columns int
	if err = a.db.QueryRow(`SELECT version,(SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='budget_publication_operations' AND column_name='reconcile_after') FROM budget_schema`).Scan(&version, &columns); err != nil || version != 22 || columns != 0 {
		t.Fatalf("partial v23 migration: %d %d %v", version, columns, err)
	}
	var after string
	if err = a.db.QueryRow(`SELECT to_jsonb(o)::text FROM budget_publication_operations o WHERE id=$1`, op.ID).Scan(&after); err != nil || after != before {
		t.Fatalf("failed migration changed uncertain operation: %v", err)
	}
	if _, err = a.db.Exec(`DROP TRIGGER fail_v23_commit ON budget_schema`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = Migrate(ctx, a.db); err != nil {
			t.Fatal(err)
		}
	}
	reconciliation, err := a.ClaimUncertainPublication(ctx, "migrated-reconciler", time.Minute)
	if err != nil || reconciliation == nil || reconciliation.Operation.ID != op.ID || reconciliation.Fence <= claim.Fence {
		t.Fatalf("migrated uncertain operation is not fenced and claimable: %#v %v", reconciliation, err)
	}
	if err = a.db.QueryRow(`SELECT version FROM budget_schema`).Scan(&version); err != nil || version != 23 {
		t.Fatalf("v23 migration version: %d %v", version, err)
	}
}
