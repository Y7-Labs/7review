package accounting

import (
	"context"
	"testing"
)

func TestV12MigrationRollsBackAndPreservesPriorTerminalEvidence(t *testing.T) {
	a, i, _ := recordedReductionFixture(t)
	ctx := context.Background()
	if _, err := a.RecoverModelReductions(ctx, 128, &countedModelReducer{}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`ALTER TABLE budget_jobs DROP COLUMN tool_observation_id; DROP TABLE budget_tool_observation_scopes,budget_model_tool_observations; ALTER TABLE budget_jobs DROP COLUMN model_batch_id; DROP TABLE budget_model_batches; ALTER TABLE budget_jobs DROP COLUMN terminal_reduction_id; UPDATE budget_schema SET version=11; CREATE FUNCTION fail_v12_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=12 THEN RAISE EXCEPTION 'injected v12 commit failure'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER fail_v12_commit AFTER UPDATE ON budget_schema DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_v12_commit()`); err != nil {
		t.Fatal(err)
	}
	if tx, err := a.begin(ctx); err == nil {
		tx.Rollback()
		t.Fatal("runtime silently accepted v11")
	}
	if err := Migrate(ctx, a.db); err == nil {
		t.Fatal("failed v12 commit reported success")
	}
	var version, columns int
	if err := a.db.QueryRow(`SELECT version FROM budget_schema`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='budget_jobs' AND column_name='terminal_reduction_id'`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if version != 11 || columns != 0 {
		t.Fatalf("partial terminal migration: %d %d", version, columns)
	}
	if _, err := a.db.Exec(`DROP TRIGGER fail_v12_commit ON budget_schema`); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 2; n++ {
		if err := Migrate(ctx, a.db); err != nil {
			t.Fatal(err)
		}
	}
	if p, err := a.CompleteModelJob(ctx, i.ActionID); err != nil || p == nil || !p.Succeeded {
		t.Fatalf("v11 evidence lost after upgrade: %#v %v", p, err)
	}
}
