package accounting

import (
	"context"
	"testing"

	"github.com/Y4NN777/7review/agent/pipeline"
)

func TestPostgresV11UpgradeRollsBackAndNeverCommitsDanglingChildGrant(t *testing.T) {
	a, parent, old := formatterParentFixture(t)
	ctx := context.Background()
	if _, err := a.db.Exec(`DROP TABLE budget_model_tool_children; ALTER TABLE budget_jobs DROP COLUMN tool_observation_id; DROP TABLE budget_tool_observation_scopes,budget_model_tool_observations; ALTER TABLE budget_jobs DROP COLUMN model_batch_id; DROP TABLE budget_model_batches; ALTER TABLE budget_jobs DROP COLUMN terminal_reduction_id; DROP TABLE budget_model_continuations; ALTER TABLE budget_jobs ALTER CONSTRAINT budget_jobs_recovery_action_id_fkey NOT DEFERRABLE; UPDATE budget_schema SET version=10; CREATE FUNCTION fail_v11_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=11 THEN RAISE EXCEPTION 'injected v11 commit failure'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER fail_v11_commit AFTER UPDATE ON budget_schema DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_v11_commit()`); err != nil {
		t.Fatal(err)
	}
	if tx, err := a.begin(ctx); err == nil {
		tx.Rollback()
		t.Fatal("runtime accepted old schema without migration")
	}
	if err := Migrate(ctx, a.db); err == nil {
		t.Fatal("failed v11 commit reported success")
	}
	var version, tables int
	var deferrable bool
	if err := a.db.QueryRow(`SELECT version FROM budget_schema`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT count(*) FROM pg_class WHERE relnamespace=current_schema()::regnamespace AND relname='budget_model_continuations'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT condeferrable FROM pg_constraint WHERE conrelid='budget_jobs'::regclass AND conname='budget_jobs_recovery_action_id_fkey'`).Scan(&deferrable); err != nil {
		t.Fatal(err)
	}
	if version != 10 || tables != 0 || deferrable {
		t.Fatalf("partial v11 upgrade: version=%d table=%d deferred=%v", version, tables, deferrable)
	}
	if _, err := a.db.Exec(`DROP TRIGGER fail_v11_commit ON budget_schema`); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		if err := Migrate(ctx, a.db); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := a.begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE budget_jobs SET recovery_action_id='missing-child'`); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err == nil {
		t.Fatal("dangling action restriction committed")
	}
	var token, grant string
	if err = a.db.QueryRow(`SELECT i.claim_token,j.recovery_action_id FROM budget_inbox i JOIN budget_jobs j USING(run_id)`).Scan(&token, &grant); err != nil {
		t.Fatal(err)
	}
	if token != old.Token || grant != parent.ActionID {
		t.Fatal("failed deferred grant changed current ownership")
	}
	if child, err := a.AdvanceModelContinuation(ctx, parent.ActionID, pipeline.ModelContinuationV1{}); err != nil || child.ID == "" {
		t.Fatalf("v10 parent lost at upgrade: %#v %v", child, err)
	}
}
