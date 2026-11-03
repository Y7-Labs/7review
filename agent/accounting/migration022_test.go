package accounting

import (
	"context"
	"testing"
)

func TestV22MigrationIsExplicitAtomicAndDoesNotInventDeliveryReceipt(t *testing.T) {
	a, _, op, _ := publicationOperationFixture(t)
	ctx := context.Background()
	var before string
	if err := a.db.QueryRow(`SELECT jsonb_build_object('operation',(SELECT to_jsonb(o)-'delivery_receipt'-'delivery_receipt_digest'-'completed_at'-'reconcile_after' FROM budget_publication_operations o WHERE id=$1),'jobs',(SELECT jsonb_agg(to_jsonb(j)) FROM budget_jobs j))::text`, op.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`ALTER TABLE budget_publication_operations DROP CONSTRAINT budget_publication_operation_reconciliation,DROP COLUMN reconcile_after,DROP CONSTRAINT budget_publication_operation_completion,DROP COLUMN delivery_receipt,DROP COLUMN delivery_receipt_digest,DROP COLUMN completed_at; UPDATE budget_schema SET version=21; CREATE FUNCTION fail_v22_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=22 THEN RAISE EXCEPTION 'injected v22 commit failure'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER fail_v22_commit AFTER UPDATE ON budget_schema DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_v22_commit()`); err != nil {
		t.Fatal(err)
	}
	if tx, err := a.begin(ctx); err == nil {
		tx.Rollback()
		t.Fatal("runtime accepted unmigrated v21")
	}
	if err := Migrate(ctx, a.db); err == nil {
		t.Fatal("failed v22 migration reported success")
	}
	var version, columns int
	if err := a.db.QueryRow(`SELECT version,(SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='budget_publication_operations' AND column_name IN ('delivery_receipt','delivery_receipt_digest','completed_at')) FROM budget_schema`).Scan(&version, &columns); err != nil || version != 21 || columns != 0 {
		t.Fatalf("partial v22 migration: %d %d %v", version, columns, err)
	}
	var after string
	if err := a.db.QueryRow(`SELECT jsonb_build_object('operation',(SELECT to_jsonb(o) FROM budget_publication_operations o WHERE id=$1),'jobs',(SELECT jsonb_agg(to_jsonb(j)) FROM budget_jobs j))::text`, op.ID).Scan(&after); err != nil || after != before {
		t.Fatalf("failed migration changed publication authority: %v", err)
	}
	if _, err := a.db.Exec(`DROP TRIGGER fail_v22_commit ON budget_schema`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(ctx, a.db); err != nil {
			t.Fatal(err)
		}
	}
	var hasReceipt, hasCompletion bool
	if err := a.db.QueryRow(`SELECT version,(SELECT delivery_receipt IS NOT NULL FROM budget_publication_operations WHERE id=$1),(SELECT completed_at IS NOT NULL FROM budget_publication_operations WHERE id=$1) FROM budget_schema`, op.ID).Scan(&version, &hasReceipt, &hasCompletion); err != nil || version != 23 || hasReceipt || hasCompletion {
		t.Fatalf("v22 invented publication completion: %d %v %v %v", version, hasReceipt, hasCompletion, err)
	}
}

func TestV22MigrationRejectsUnreceiptedDeliveredState(t *testing.T) {
	a, _, op, _ := publicationOperationFixture(t)
	if _, err := a.db.Exec(`ALTER TABLE budget_publication_operations DROP CONSTRAINT budget_publication_operation_reconciliation,DROP COLUMN reconcile_after,DROP CONSTRAINT budget_publication_operation_completion,DROP COLUMN delivery_receipt,DROP COLUMN delivery_receipt_digest,DROP COLUMN completed_at; UPDATE budget_schema SET version=21`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`UPDATE budget_publication_operations SET state='delivered',worker_id='legacy-worker',fence=1,lease_until=clock_timestamp(),sending_at=clock_timestamp() WHERE id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), a.db); err == nil {
		t.Fatal("migration invented a receipt for an unproven delivered state")
	}
	var version int
	var state string
	if err := a.db.QueryRow(`SELECT version,(SELECT state FROM budget_publication_operations WHERE id=$1) FROM budget_schema`, op.ID).Scan(&version, &state); err != nil || version != 21 || state != "delivered" {
		t.Fatalf("rejected migration changed legacy state: %d %s %v", version, state, err)
	}
}
