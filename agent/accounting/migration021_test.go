package accounting

import (
	"context"
	"testing"
	"time"
)

func TestV21MigrationIsExplicitAtomicAndDoesNotInventPublicationLeaseOrSending(t *testing.T) {
	a, command, op, _ := publicationOperationFixture(t)
	ctx := context.Background()
	dropFindingDispositionSchemaForMigration(t, a.db)
	var before string
	if err := a.db.QueryRow(`SELECT jsonb_build_object('operations',(SELECT jsonb_agg(jsonb_build_object('id',id,'authorization_id',authorization_id,'command_id',command_id,'run_id',run_id,'attempt_id',attempt_id,'assessment_digest',assessment_digest,'artifact_digest',artifact_digest,'destination',encode(destination,'hex'),'destination_digest',destination_digest,'payload',encode(payload,'hex'),'payload_digest',payload_digest,'state',state,'created_at',created_at,'updated_at',updated_at)) FROM budget_publication_operations),'jobs',(SELECT jsonb_agg(to_jsonb(j)) FROM budget_jobs j),'commands',(SELECT jsonb_agg(to_jsonb(c)) FROM budget_operator_commands c))::text`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`ALTER TABLE budget_publication_operations DROP CONSTRAINT budget_publication_operation_reconciliation,DROP COLUMN reconcile_after,DROP CONSTRAINT budget_publication_operation_completion,DROP COLUMN delivery_receipt,DROP COLUMN delivery_receipt_digest,DROP COLUMN completed_at,DROP CONSTRAINT budget_publication_operation_lease,DROP CONSTRAINT budget_publication_operation_sending,DROP CONSTRAINT budget_publication_operations_fence_check,DROP COLUMN worker_id,DROP COLUMN fence,DROP COLUMN lease_until,DROP COLUMN sending_at; UPDATE budget_schema SET version=20; CREATE FUNCTION fail_v21_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=21 THEN RAISE EXCEPTION 'injected v21 commit failure'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER fail_v21_commit AFTER UPDATE ON budget_schema DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_v21_commit()`); err != nil {
		t.Fatal(err)
	}
	if tx, err := a.begin(ctx); err == nil {
		tx.Rollback()
		t.Fatal("runtime accepted unmigrated v20")
	}
	if err := Migrate(ctx, a.db); err == nil {
		t.Fatal("failed v21 migration reported success")
	}
	var version, columns int
	if err := a.db.QueryRow(`SELECT version,(SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='budget_publication_operations' AND column_name IN ('worker_id','fence','lease_until','sending_at')) FROM budget_schema`).Scan(&version, &columns); err != nil || version != 20 || columns != 0 {
		t.Fatalf("partial v21 migration: %d %d %v", version, columns, err)
	}
	var after string
	if err := a.db.QueryRow(`SELECT jsonb_build_object('operations',(SELECT jsonb_agg(jsonb_build_object('id',id,'authorization_id',authorization_id,'command_id',command_id,'run_id',run_id,'attempt_id',attempt_id,'assessment_digest',assessment_digest,'artifact_digest',artifact_digest,'destination',encode(destination,'hex'),'destination_digest',destination_digest,'payload',encode(payload,'hex'),'payload_digest',payload_digest,'state',state,'created_at',created_at,'updated_at',updated_at)) FROM budget_publication_operations),'jobs',(SELECT jsonb_agg(to_jsonb(j)) FROM budget_jobs j),'commands',(SELECT jsonb_agg(to_jsonb(c)) FROM budget_operator_commands c))::text`).Scan(&after); err != nil || after != before {
		t.Fatalf("failed migration changed outbox authority: %v", err)
	}
	if _, err := a.db.Exec(`DROP TRIGGER fail_v21_commit ON budget_schema`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(ctx, a.db); err != nil {
			t.Fatal(err)
		}
	}
	var worker *string
	var lease, sending *time.Time
	var fence uint64
	if err := a.db.QueryRow(`SELECT version,(SELECT worker_id FROM budget_publication_operations WHERE id=$1),(SELECT fence FROM budget_publication_operations WHERE id=$1),(SELECT lease_until FROM budget_publication_operations WHERE id=$1),(SELECT sending_at FROM budget_publication_operations WHERE id=$1) FROM budget_schema`, op.ID).Scan(&version, &worker, &fence, &lease, &sending); err != nil || version != 24 || worker != nil || fence != 0 || lease != nil || sending != nil {
		t.Fatalf("v21 invented publication ownership/sending: %d %v %d %v %v %v", version, worker, fence, lease, sending, err)
	}
	claim, err := a.ClaimPublication(ctx, "migrated-worker", time.Minute)
	if err != nil || claim == nil || claim.Operation.AuthorizationCommandID != command.CommandID {
		t.Fatalf("migrated pending operation cannot be claimed: %#v %v", claim, err)
	}
}
