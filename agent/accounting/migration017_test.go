package accounting

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func dropUnusedSchemaForMigration(t *testing.T, db *sql.DB) {
	t.Helper()
	dropSupersessionSchemaForMigration(t, db)
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM budget_unused_releases`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("cannot erase release proofs for historical fixture: %d %v", count, err)
	}
	if _, err := db.Exec(`ALTER TABLE budget_actions DROP CONSTRAINT budget_actions_unused_release; ALTER TABLE budget_actions DROP COLUMN unused_release_id; DROP TABLE budget_unused_release_scopes,budget_unused_releases; ALTER TABLE budget_actions DROP CONSTRAINT budget_actions_state_check; ALTER TABLE budget_actions ADD CONSTRAINT budget_actions_state_check CHECK(state IN ('prepared','sending','uncertain','result_recorded'))`); err != nil {
		t.Fatal(err)
	}
}

func dropSupersessionSchemaForMigration(t *testing.T, db *sql.DB) {
	t.Helper()
	dropPublicationAuthorizationSchemaForMigration(t, db)
	var columns int
	if err := db.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='budget_unused_releases' AND column_name='cause'`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if columns == 0 {
		return
	}
	var superseded int
	if err := db.QueryRow(`SELECT count(*) FROM budget_unused_releases WHERE cause='superseded'`).Scan(&superseded); err != nil || superseded != 0 {
		t.Fatalf("cannot erase supersession proofs for historical fixture: %d %v", superseded, err)
	}
	if _, err := db.Exec(`ALTER TABLE budget_unused_releases DROP CONSTRAINT budget_unused_release_authority; ALTER TABLE budget_unused_releases DROP COLUMN cause,DROP COLUMN superseding_attempt_id,DROP COLUMN supersession_intake_id; ALTER TABLE budget_unused_releases ALTER COLUMN cancel_command_id SET NOT NULL; ALTER TABLE budget_attempts DROP CONSTRAINT budget_attempts_supersession; ALTER TABLE budget_attempts DROP COLUMN superseded_by_attempt_id,DROP COLUMN supersession_intake_id; ALTER TABLE budget_inbox DROP CONSTRAINT budget_inbox_state_check; ALTER TABLE budget_inbox ADD CONSTRAINT budget_inbox_state_check CHECK(state IN ('pending','running','completed','failed','expired','uncertain','cancelled'))`); err != nil {
		t.Fatal(err)
	}
}

func dropPublicationAuthorizationSchemaForMigration(t *testing.T, db *sql.DB) {
	t.Helper()
	dropPublicationOperationSchemaForMigration(t, db)
	var columns int
	if err := db.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='budget_jobs' AND column_name='publication_authorization_id'`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if columns == 0 {
		return
	}
	var linked int
	if err := db.QueryRow(`SELECT count(*) FROM budget_jobs WHERE publication_authorization_id IS NOT NULL`).Scan(&linked); err != nil || linked != 0 {
		t.Fatalf("cannot erase publication authorizations for historical fixture: %d %v", linked, err)
	}
	if _, err := db.Exec(`ALTER TABLE budget_jobs DROP CONSTRAINT budget_jobs_publication_authorization; ALTER TABLE budget_jobs DROP COLUMN publication_authorization_id`); err != nil {
		t.Fatal(err)
	}
}

func dropPublicationOperationSchemaForMigration(t *testing.T, db *sql.DB) {
	t.Helper()
	dropFindingDispositionSchemaForMigration(t, db)
	var tables int
	if err := db.QueryRow(`SELECT count(*) FROM pg_class WHERE relnamespace=current_schema()::regnamespace AND relname='budget_publication_operations'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables == 0 {
		return
	}
	var operations int
	if err := db.QueryRow(`SELECT count(*) FROM budget_publication_operations`).Scan(&operations); err != nil || operations != 0 {
		t.Fatalf("cannot erase publication operations for historical fixture: %d %v", operations, err)
	}
	if _, err := db.Exec(`DROP TABLE budget_publication_operations`); err != nil {
		t.Fatal(err)
	}
}

func TestV17MigrationPreservesCancellationSendingEvidenceAndHoldsWithoutInferringRelease(t *testing.T) {
	a, i, actor, c, _ := cancellationFixture(t)
	ctx := context.Background()
	unused, err := a.PrepareAction(ctx, i)
	if err != nil {
		t.Fatal(err)
	}
	sending := i
	sending.ActionID = "migration-sending"
	p, err := a.PrepareAction(ctx, sending)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := a.ClaimPrepared(ctx, p, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.BeginPrepared(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if _, err = a.CancelAttempt(ctx, actor, c); err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		t.Helper()
		var raw string
		if err := a.db.QueryRow(`SELECT jsonb_build_object('actions',(SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM budget_actions a),'holds',(SELECT jsonb_agg(to_jsonb(h) ORDER BY action_id,scope_id) FROM budget_holds h),'leases',(SELECT jsonb_agg(to_jsonb(l) ORDER BY action_id) FROM budget_action_leases l),'commands',(SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM budget_operator_commands c),'jobs',(SELECT jsonb_agg(to_jsonb(j) ORDER BY run_id) FROM budget_jobs j),'inbox',(SELECT jsonb_agg(to_jsonb(i) ORDER BY run_id) FROM budget_inbox i))::text`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	before := snapshot()
	dropUnusedSchemaForMigration(t, a.db)
	if _, err = a.db.Exec(`UPDATE budget_schema SET version=16; CREATE FUNCTION fail_v17_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=17 THEN RAISE EXCEPTION 'injected v17 commit failure'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER fail_v17_commit AFTER UPDATE ON budget_schema DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_v17_commit()`); err != nil {
		t.Fatal(err)
	}
	if tx, err := a.begin(ctx); err == nil {
		tx.Rollback()
		t.Fatal("runtime accepted unmigrated v16")
	}
	if err = Migrate(ctx, a.db); err == nil {
		t.Fatal("uncommitted migration reported success")
	}
	var version, tables, columns int
	if err = a.db.QueryRow(`SELECT version,(SELECT count(*) FROM pg_class WHERE relnamespace=current_schema()::regnamespace AND relname IN ('budget_unused_releases','budget_unused_release_scopes')),(SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='budget_actions' AND column_name='unused_release_id') FROM budget_schema`).Scan(&version, &tables, &columns); err != nil || version != 16 || tables != 0 || columns != 0 {
		t.Fatalf("partial release migration: %d %d %d %v", version, tables, columns, err)
	}
	if _, err = a.db.Exec(`DROP TRIGGER fail_v17_commit ON budget_schema`); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 2; n++ {
		if err = Migrate(ctx, a.db); err != nil {
			t.Fatal(err)
		}
	}
	if snapshot() != before {
		t.Fatal("migration rewrote cancellation/evidence or released holds")
	}
	var proofs int
	if err = a.db.QueryRow(`SELECT count(*) FROM budget_unused_releases`).Scan(&proofs); err != nil || proofs != 0 {
		t.Fatalf("migration inferred a refund: %d %v", proofs, err)
	}
	if _, err = a.ReleaseCancelledPrepared(ctx, unused.ID); err != nil {
		t.Fatalf("migrated cancellation cannot release verified unused reservation: %v", err)
	}
	if _, err = a.ReleaseCancelledPrepared(ctx, p.ID); err == nil {
		t.Fatal("migrated sending marker was refunded")
	}
}
