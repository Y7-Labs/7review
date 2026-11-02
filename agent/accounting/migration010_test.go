package accounting

import (
	"context"
	"testing"
)

func TestPostgresV10MigrationRollsBackAndReducesExistingV9Results(t *testing.T) {
	a, i, _ := recordedReductionFixture(t)
	ctx := context.Background()
	var before string
	if err := a.db.QueryRow(`SELECT md5(request)||md5(quote)||md5(outcome) FROM budget_actions WHERE id=$1`, i.ActionID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`DROP TABLE budget_model_continuations,budget_model_reduction_inputs,budget_model_reductions; ALTER TABLE budget_jobs ALTER CONSTRAINT budget_jobs_recovery_action_id_fkey NOT DEFERRABLE; UPDATE budget_schema SET version=9; CREATE FUNCTION fail_v10_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=10 THEN RAISE EXCEPTION 'injected v10 commit failure'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER fail_v10_commit AFTER UPDATE ON budget_schema DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_v10_commit()`); err != nil {
		t.Fatal(err)
	}
	if tx, err := a.begin(ctx); err == nil {
		tx.Rollback()
		t.Fatal("runtime implicitly upgraded v9")
	}
	if err := Migrate(ctx, a.db); err == nil {
		t.Fatal("failed migration reported success")
	}
	var version, tables int
	if err := a.db.QueryRow(`SELECT version FROM budget_schema`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT count(*) FROM pg_class WHERE relnamespace=current_schema()::regnamespace AND relname IN ('budget_model_reductions','budget_model_reduction_inputs')`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if version != 9 || tables != 0 {
		t.Fatalf("partial v10 upgrade: version=%d tables=%d", version, tables)
	}
	if _, err := a.db.Exec(`DROP TRIGGER fail_v10_commit ON budget_schema`); err != nil {
		t.Fatal(err)
	}
	for count := 0; count < 2; count++ {
		if err := Migrate(ctx, a.db); err != nil {
			t.Fatal(err)
		}
	}
	var after string
	if err := a.db.QueryRow(`SELECT md5(request)||md5(quote)||md5(outcome) FROM budget_actions WHERE id=$1`, i.ActionID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("migration rewrote immutable action or recorded response")
	}
	if n, err := a.RecoverModelReductions(ctx, 128, &countedModelReducer{}); err != nil || n != 1 {
		t.Fatalf("v9 response lost or resent after upgrade: %d %v", n, err)
	}
	var actions, holds int
	_ = a.db.QueryRow(`SELECT count(*) FROM budget_actions`).Scan(&actions)
	_ = a.db.QueryRow(`SELECT count(*) FROM budget_holds`).Scan(&holds)
	if actions != 1 || holds != 3 {
		t.Fatalf("upgrade reset spending: actions=%d holds=%d", actions, holds)
	}
}
