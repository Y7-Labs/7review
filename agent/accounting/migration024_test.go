package accounting

import (
	"context"
	"database/sql"
	"testing"
)

func dropFindingDispositionSchemaForMigration(t *testing.T, db *sql.DB) {
	t.Helper()
	var tables int
	if err := db.QueryRow(`SELECT count(*) FROM pg_class WHERE relnamespace=current_schema()::regnamespace AND relname IN ('budget_finding_dispositions','budget_finding_disposition_heads')`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables == 0 {
		return
	}
	var dispositions int
	if err := db.QueryRow(`SELECT count(*) FROM budget_finding_dispositions`).Scan(&dispositions); err != nil || dispositions != 0 {
		t.Fatalf("cannot erase finding dispositions for historical fixture: %d %v", dispositions, err)
	}
	if _, err := db.Exec(`DROP TABLE budget_finding_disposition_heads,budget_finding_dispositions`); err != nil {
		t.Fatal(err)
	}
}

func TestV24MigrationIsExplicitAtomicAndDoesNotInventFindingDispositions(t *testing.T) {
	a, intent, actor, _, command, _ := findingDispositionFixture(t)
	ctx := context.Background()
	var before string
	if err := a.db.QueryRow(`SELECT jsonb_build_object('job',(SELECT to_jsonb(j) FROM budget_jobs j WHERE run_id=$1),'commands',(SELECT jsonb_agg(to_jsonb(c)) FROM budget_operator_commands c),'reductions',(SELECT jsonb_agg(to_jsonb(m)) FROM budget_model_reductions m))::text`, intent.Job.RunID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	dropFindingDispositionSchemaForMigration(t, a.db)
	if _, err := a.db.Exec(`UPDATE budget_schema SET version=23; CREATE FUNCTION fail_v24_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=24 THEN RAISE EXCEPTION 'injected v24 commit failure'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER fail_v24_commit AFTER UPDATE ON budget_schema DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_v24_commit()`); err != nil {
		t.Fatal(err)
	}
	if tx, err := a.begin(ctx); err == nil {
		tx.Rollback()
		t.Fatal("runtime accepted unmigrated v23")
	}
	if err := Migrate(ctx, a.db); err == nil {
		t.Fatal("failed v24 migration reported success")
	}
	var version, tables int
	if err := a.db.QueryRow(`SELECT version,(SELECT count(*) FROM pg_class WHERE relnamespace=current_schema()::regnamespace AND relname IN ('budget_finding_dispositions','budget_finding_disposition_heads')) FROM budget_schema`).Scan(&version, &tables); err != nil || version != 23 || tables != 0 {
		t.Fatalf("partial v24 migration: %d %d %v", version, tables, err)
	}
	var after string
	if err := a.db.QueryRow(`SELECT jsonb_build_object('job',(SELECT to_jsonb(j) FROM budget_jobs j WHERE run_id=$1),'commands',(SELECT jsonb_agg(to_jsonb(c)) FROM budget_operator_commands c),'reductions',(SELECT jsonb_agg(to_jsonb(m)) FROM budget_model_reductions m))::text`, intent.Job.RunID).Scan(&after); err != nil || after != before {
		t.Fatalf("failed migration changed terminal authority: %v", err)
	}
	if _, err := a.db.Exec(`DROP TRIGGER fail_v24_commit ON budget_schema`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(ctx, a.db); err != nil {
			t.Fatal(err)
		}
	}
	var dispositions int
	if err := a.db.QueryRow(`SELECT version,(SELECT count(*) FROM budget_finding_dispositions) FROM budget_schema`).Scan(&version, &dispositions); err != nil || version != 24 || dispositions != 0 {
		t.Fatalf("v24 invented finding dispositions: %d %d %v", version, dispositions, err)
	}
	if receipt, err := a.DispositionFinding(ctx, actor, command); err != nil || receipt == nil || receipt.ControlVersion != 3 {
		t.Fatalf("migrated terminal finding cannot be dispositioned: %#v %v", receipt, err)
	}
}
