package accounting

import (
	"bytes"
	"context"
	"testing"

	"github.com/Y4NN777/7review/agent/pipeline"
)

func TestV14MigrationIsExplicitAtomicAndPreservesUnchargedParents(t *testing.T) {
	a, i, old := toolParentFixture(t)
	ctx := context.Background()
	var request, outcome, reduction []byte
	if err := a.db.QueryRow(`SELECT request,outcome,(SELECT artifact FROM budget_model_reductions) FROM budget_actions`).Scan(&request, &outcome, &reduction); err != nil {
		t.Fatal(err)
	}
	dropOperatorSchemaForMigration(t, a.db)
	if _, err := a.db.Exec(`DROP TABLE budget_model_tool_children; ALTER TABLE budget_jobs DROP COLUMN tool_observation_id; DROP TABLE budget_tool_observation_scopes,budget_model_tool_observations; UPDATE budget_schema SET version=13; CREATE FUNCTION fail_v14_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=14 THEN RAISE EXCEPTION 'injected v14 commit failure'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER fail_v14_commit AFTER UPDATE ON budget_schema DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_v14_commit()`); err != nil {
		t.Fatal(err)
	}
	if tx, err := a.begin(ctx); err == nil {
		tx.Rollback()
		t.Fatal("runtime silently accepted unmigrated v13")
	}
	if err := Migrate(ctx, a.db); err == nil {
		t.Fatal("failed migration commit reported success")
	}
	var version, tables, columns int
	if err := a.db.QueryRow(`SELECT version,(SELECT count(*) FROM pg_class WHERE relnamespace=current_schema()::regnamespace AND relname IN ('budget_model_tool_observations','budget_tool_observation_scopes')),(SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='budget_jobs' AND column_name='tool_observation_id') FROM budget_schema`).Scan(&version, &tables, &columns); err != nil || version != 13 || tables != 0 || columns != 0 {
		t.Fatalf("partial observation migration: %d %d %d %v", version, tables, columns, err)
	}
	if _, err := a.db.Exec(`DROP TRIGGER fail_v14_commit ON budget_schema`); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 2; n++ {
		if err := Migrate(ctx, a.db); err != nil {
			t.Fatal(err)
		}
	}
	var migratedRequest, migratedOutcome, migratedReduction []byte
	var token string
	var records, links, holds int
	var pointerMissing bool
	if err := a.db.QueryRow(`SELECT a.request,a.outcome,m.artifact,i.claim_token,j.tool_observation_id IS NULL,(SELECT count(*) FROM budget_model_tool_observations),(SELECT count(*) FROM budget_tool_observation_scopes),(SELECT count(*) FROM budget_holds) FROM budget_actions a CROSS JOIN budget_model_reductions m CROSS JOIN budget_inbox i JOIN budget_jobs j ON j.run_id=i.run_id`).Scan(&migratedRequest, &migratedOutcome, &migratedReduction, &token, &pointerMissing, &records, &links, &holds); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(request, migratedRequest) || !bytes.Equal(outcome, migratedOutcome) || !bytes.Equal(reduction, migratedReduction) || token != old.Token || !pointerMissing || records != 0 || links != 0 || holds != 3 {
		t.Fatal("migration inferred charges/authority or rewrote legacy evidence/holds")
	}
	if _, err := a.CommitModelToolObservations(ctx, i.ActionID, pipeline.ModelToolObserverV1{}); err != nil {
		t.Fatalf("migrated parent cannot commit checked observations: %v", err)
	}
	if _, err := a.CheckInbox(ctx); err != nil {
		t.Fatal(err)
	}
}
