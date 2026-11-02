package accounting

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestV18MigrationIsExplicitAtomicPreservesV17ProofAndInfersNoSupersession(t *testing.T) {
	a, i, actor, command, _ := cancellationFixture(t)
	ctx := context.Background()
	prepared, err := a.PrepareAction(ctx, i)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.CancelAttempt(ctx, actor, command); err != nil {
		t.Fatal(err)
	}
	release, err := a.ReleaseCancelledPrepared(ctx, prepared.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce immutable v17 bytes, which did not carry an explicit Cause.
	release.Cause = ""
	legacyRaw, err := json.Marshal(release)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(`UPDATE budget_unused_releases SET receipt=$1,receipt_digest=$2 WHERE action_id=$3`, legacyRaw, hash(legacyRaw), prepared.ID); err != nil {
		t.Fatal(err)
	}
	dropSupersessionSchemaForMigration(t, a.db)
	if _, err = a.db.Exec(`UPDATE budget_schema SET version=17; CREATE FUNCTION fail_v18_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=18 THEN RAISE EXCEPTION 'injected v18 commit failure'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER fail_v18_commit AFTER UPDATE ON budget_schema DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_v18_commit()`); err != nil {
		t.Fatal(err)
	}
	if tx, beginErr := a.begin(ctx); beginErr == nil {
		tx.Rollback()
		t.Fatal("runtime accepted unmigrated v17")
	}
	if err = Migrate(ctx, a.db); err == nil {
		t.Fatal("failed v18 migration reported success")
	}
	var version, newColumns int
	var stored []byte
	if err = a.db.QueryRow(`SELECT version,(SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND ((table_name='budget_attempts' AND column_name IN ('superseded_by_attempt_id','supersession_intake_id')) OR (table_name='budget_unused_releases' AND column_name IN ('cause','superseding_attempt_id','supersession_intake_id')))),(SELECT receipt FROM budget_unused_releases WHERE action_id=$1) FROM budget_schema`, prepared.ID).Scan(&version, &newColumns, &stored); err != nil || version != 17 || newColumns != 0 || !reflect.DeepEqual(stored, legacyRaw) {
		t.Fatalf("partial v18 migration: %d %d %v", version, newColumns, err)
	}
	if _, err = a.db.Exec(`DROP TRIGGER fail_v18_commit ON budget_schema`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = Migrate(ctx, a.db); err != nil {
			t.Fatal(err)
		}
	}
	var cause string
	var linked, inferred, publication int
	if err = a.db.QueryRow(`SELECT version,(SELECT cause FROM budget_unused_releases WHERE action_id=$1),(SELECT count(*) FROM budget_attempts WHERE superseded_by_attempt_id IS NOT NULL OR supersession_intake_id IS NOT NULL),(SELECT count(*) FROM budget_unused_releases WHERE cause='superseded'),(SELECT count(*) FROM budget_jobs WHERE publication_authorization_id IS NOT NULL) FROM budget_schema`, prepared.ID).Scan(&version, &cause, &linked, &inferred, &publication); err != nil || version != 20 || cause != "cancelled" || linked != 0 || inferred != 0 || publication != 0 {
		t.Fatalf("v18/v19 inferred authority: %d %s %d %d %d %v", version, cause, linked, inferred, publication, err)
	}
	var after []byte
	if err = a.db.QueryRow(`SELECT receipt FROM budget_unused_releases WHERE action_id=$1`, prepared.ID).Scan(&after); err != nil || !reflect.DeepEqual(after, legacyRaw) {
		t.Fatal("v18 rewrote immutable release receipt")
	}
	replayed, err := a.ReleaseCancelledPrepared(ctx, prepared.ID)
	if err != nil || !reflect.DeepEqual(replayed, release) {
		t.Fatalf("migrated v17 proof did not replay: %+v %v", replayed, err)
	}
}
