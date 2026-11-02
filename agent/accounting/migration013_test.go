package accounting

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/orchestrator"
)

func TestV13MigrationRollsBackAndDoesNotInventLegacyBatchAuthority(t *testing.T) {
	a, intents := modelBatchFixture(t)
	ctx := context.Background()
	prepared, err := a.PrepareModelBatch(ctx, intents)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := a.ClaimPrepared(ctx, prepared[0], "v12-sender", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	permit, err := a.BeginPrepared(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	var request, quote []byte
	if err = a.db.QueryRow(`SELECT request,quote FROM budget_actions WHERE id=$1`, prepared[0].ID).Scan(&request, &quote); err != nil {
		t.Fatal(err)
	}
	dropOperatorSchemaForMigration(t, a.db)
	if _, err = a.db.Exec(`DROP TABLE budget_model_tool_children; ALTER TABLE budget_jobs DROP COLUMN tool_observation_id; DROP TABLE budget_tool_observation_scopes,budget_model_tool_observations; ALTER TABLE budget_jobs DROP COLUMN model_batch_id; DROP TABLE budget_model_batches; UPDATE budget_schema SET version=12; CREATE FUNCTION fail_v13_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=13 THEN RAISE EXCEPTION 'injected v13 commit failure'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER fail_v13_commit AFTER UPDATE ON budget_schema DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_v13_commit()`); err != nil {
		t.Fatal(err)
	}
	if tx, err := a.begin(ctx); err == nil {
		tx.Rollback()
		t.Fatal("runtime accepted unmigrated v12")
	}
	if err = Migrate(ctx, a.db); err == nil {
		t.Fatal("failed schema commit reported success")
	}
	var version, tables, columns int
	if err = a.db.QueryRow(`SELECT version,(SELECT count(*) FROM pg_class WHERE relnamespace=current_schema()::regnamespace AND relname='budget_model_batches'),(SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='budget_jobs' AND column_name='model_batch_id') FROM budget_schema`).Scan(&version, &tables, &columns); err != nil {
		t.Fatal(err)
	}
	if version != 12 || tables != 0 || columns != 0 {
		t.Fatalf("partial seal migration: %d %d %d", version, tables, columns)
	}
	if _, err = a.db.Exec(`DROP TRIGGER fail_v13_commit ON budget_schema`); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 2; n++ {
		if err = Migrate(ctx, a.db); err != nil {
			t.Fatal(err)
		}
	}
	var migratedRequest, migratedQuote []byte
	if err = a.db.QueryRow(`SELECT request,quote FROM budget_actions WHERE id=$1`, prepared[0].ID).Scan(&migratedRequest, &migratedQuote); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(request, migratedRequest) || !bytes.Equal(quote, migratedQuote) {
		t.Fatal("migration rewrote immutable request/quote")
	}
	var seals, holds int
	if err = a.db.QueryRow(`SELECT (SELECT count(*) FROM budget_model_batches),(SELECT count(*) FROM budget_holds)`).Scan(&seals, &holds); err != nil || seals != 0 || holds != 6 {
		t.Fatalf("migration invented a seal or reset holds: %d %d %v", seals, holds, err)
	}
	if _, err = a.ReclaimPrepared(ctx, prepared[1], "not-a-continuation", time.Minute); err == nil {
		t.Fatal("migration granted legacy sibling recovery")
	}
	raw := []byte("[]")
	if err = a.RecordOutcome(ctx, permit, orchestrator.DispatchOutcome{Response: raw, ResponseReceived: true, ResponseDigest: hash(raw), ElapsedMS: 1}); err != nil {
		t.Fatalf("migration lost fenced sending attribution: %v", err)
	}
}
