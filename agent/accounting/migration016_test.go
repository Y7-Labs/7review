package accounting

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// Historical upgrade rehearsals must remove later schema, not leave v16
// objects behind while pretending to be an older installation.
func dropOperatorSchemaForMigration(t *testing.T, db *sql.DB) {
	t.Helper()
	var commands int
	if err := db.QueryRow(`SELECT count(*) FROM budget_operator_commands`).Scan(&commands); err != nil || commands != 0 {
		t.Fatalf("cannot construct historical schema with command receipts: %d %v", commands, err)
	}
	if _, err := db.Exec(`ALTER TABLE budget_jobs DROP CONSTRAINT budget_jobs_cancel_disposition; ALTER TABLE budget_jobs DROP COLUMN cancel_command_id,DROP COLUMN control_version; DROP TABLE budget_operator_commands; ALTER TABLE budget_inbox DROP CONSTRAINT budget_inbox_state_check; ALTER TABLE budget_inbox ADD CONSTRAINT budget_inbox_state_check CHECK(state IN ('pending','running','completed','failed','expired','uncertain'))`); err != nil {
		t.Fatal(err)
	}
}

func TestV16MigrationIsExplicitAtomicAndDoesNotInventControlDispositions(t *testing.T) {
	a, i, actor, c, _ := cancellationFixture(t)
	ctx := context.Background()
	p, err := a.PrepareAction(ctx, i)
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
	snapshot := func() string {
		t.Helper()
		var raw string
		if err := a.db.QueryRow(`SELECT jsonb_build_object('actions',(SELECT jsonb_agg(to_jsonb(a)) FROM budget_actions a),'holds',(SELECT jsonb_agg(to_jsonb(h)) FROM budget_holds h),'leases',(SELECT jsonb_agg(to_jsonb(l)) FROM budget_action_leases l),'attempts',(SELECT jsonb_agg(to_jsonb(a)) FROM budget_attempts a),'inbox',(SELECT jsonb_agg(to_jsonb(i)) FROM budget_inbox i))::text`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	before := snapshot()
	dropOperatorSchemaForMigration(t, a.db)
	if _, err = a.db.Exec(`UPDATE budget_schema SET version=15; CREATE FUNCTION fail_v16_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=16 THEN RAISE EXCEPTION 'injected v16 commit failure'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER fail_v16_commit AFTER UPDATE ON budget_schema DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_v16_commit()`); err != nil {
		t.Fatal(err)
	}
	if tx, err := a.begin(ctx); err == nil {
		tx.Rollback()
		t.Fatal("runtime accepted unmigrated v15")
	}
	if err = Migrate(ctx, a.db); err == nil {
		t.Fatal("uncommitted migration reported success")
	}
	var version, tables, columns int
	if err = a.db.QueryRow(`SELECT version,(SELECT count(*) FROM pg_class WHERE relnamespace=current_schema()::regnamespace AND relname='budget_operator_commands'),(SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='budget_jobs' AND column_name IN ('control_version','cancel_command_id')) FROM budget_schema`).Scan(&version, &tables, &columns); err != nil || version != 15 || tables != 0 || columns != 0 {
		t.Fatalf("partial control migration: %d %d %d %v", version, tables, columns, err)
	}
	if _, err = a.db.Exec(`DROP TRIGGER fail_v16_commit ON budget_schema`); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 2; n++ {
		if err = Migrate(ctx, a.db); err != nil {
			t.Fatal(err)
		}
	}
	if after := snapshot(); after != before {
		t.Fatal("migration changed evidence, ownership or reservations")
	}
	var commands, control int
	var noCancel, allowed bool
	if err = a.db.QueryRow(`SELECT (SELECT count(*) FROM budget_operator_commands),control_version,cancel_command_id IS NULL,dispatch_allowed FROM budget_jobs`).Scan(&commands, &control, &noCancel, &allowed); err != nil || commands != 0 || control != 1 || !noCancel || !allowed {
		t.Fatalf("migration inferred control authority: %d %d %v %v %v", commands, control, noCancel, allowed, err)
	}
	if permit, err := a.BeginPrepared(ctx, claim); err != nil || !permit.AlreadyStarted {
		t.Fatalf("migration reset sending marker: %+v %v", permit, err)
	}
	if _, err = a.CancelAttempt(ctx, actor, c); err != nil {
		t.Fatalf("migrated attempt cannot be explicitly cancelled: %v", err)
	}
}
