package accounting

import (
	"bytes"
	"context"
	"github.com/Y4NN777/7review/agent/pipeline"
	"testing"
)

func TestV15MigrationPreservesAcceptedObservationsWithoutInventingChildren(t *testing.T) {
	a, i := toolChildParentFixture(t)
	ctx := context.Background()
	var receipt []byte
	var token string
	if err := a.db.QueryRow(`SELECT o.receipt,i.claim_token FROM budget_model_tool_observations o CROSS JOIN budget_inbox i`).Scan(&receipt, &token); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`DROP TABLE budget_model_tool_children; UPDATE budget_schema SET version=14; CREATE FUNCTION fail_v15_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=15 THEN RAISE EXCEPTION 'injected v15 commit failure'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER fail_v15_commit AFTER UPDATE ON budget_schema DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_v15_commit()`); err != nil {
		t.Fatal(err)
	}
	if tx, err := a.begin(ctx); err == nil {
		tx.Rollback()
		t.Fatal("runtime accepted unmigrated v14")
	}
	if err := Migrate(ctx, a.db); err == nil {
		t.Fatal("failed migration reported success")
	}
	var version, tables int
	if err := a.db.QueryRow(`SELECT version,(SELECT count(*) FROM pg_class WHERE relnamespace=current_schema()::regnamespace AND relname='budget_model_tool_children') FROM budget_schema`).Scan(&version, &tables); err != nil || version != 14 || tables != 0 {
		t.Fatalf("partial tool-child migration: %d %d %v", version, tables, err)
	}
	if _, err := a.db.Exec(`DROP TRIGGER fail_v15_commit ON budget_schema`); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 2; n++ {
		if err := Migrate(ctx, a.db); err != nil {
			t.Fatal(err)
		}
	}
	var after []byte
	var afterToken string
	var children, holds, links int
	if err := a.db.QueryRow(`SELECT o.receipt,i.claim_token,(SELECT count(*) FROM budget_model_tool_children),(SELECT count(*) FROM budget_holds),(SELECT count(*) FROM budget_tool_observation_scopes) FROM budget_model_tool_observations o CROSS JOIN budget_inbox i`).Scan(&after, &afterToken, &children, &holds, &links); err != nil || !bytes.Equal(receipt, after) || token != afterToken || children != 0 || holds != 3 || links != 3 {
		t.Fatal("migration rewrote observations/ownership or inferred a child")
	}
	if p, err := a.AdvanceModelToolContinuation(ctx, i.ActionID, pipeline.ModelToolContinuationV1{}); err != nil || p.ID == "" {
		t.Fatalf("migrated observation could not authorize a checked child: %#v %v", p, err)
	}
}
