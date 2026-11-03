package accounting

import (
	"context"
	"testing"
)

func TestV20MigrationIsExplicitAtomicAndDoesNotInventPublicationOperations(t *testing.T) {
	a, _, actor, _, command, _ := publicationAuthorizationFixture(t)
	ctx := context.Background()
	receipt, err := a.AuthorizePublication(ctx, actor, command)
	if err != nil {
		t.Fatal(err)
	}
	var before string
	if err = a.db.QueryRow(`SELECT jsonb_build_object('jobs',(SELECT jsonb_agg(to_jsonb(j)) FROM budget_jobs j),'commands',(SELECT jsonb_agg(to_jsonb(c)) FROM budget_operator_commands c),'reductions',(SELECT jsonb_agg(to_jsonb(m)) FROM budget_model_reductions m))::text`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	dropPublicationOperationSchemaForMigration(t, a.db)
	if _, err = a.db.Exec(`UPDATE budget_schema SET version=19; CREATE FUNCTION fail_v20_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=20 THEN RAISE EXCEPTION 'injected v20 commit failure'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER fail_v20_commit AFTER UPDATE ON budget_schema DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_v20_commit()`); err != nil {
		t.Fatal(err)
	}
	if tx, beginErr := a.begin(ctx); beginErr == nil {
		tx.Rollback()
		t.Fatal("runtime accepted unmigrated v19")
	}
	if err = Migrate(ctx, a.db); err == nil {
		t.Fatal("failed v20 migration reported success")
	}
	var version, tables int
	if err = a.db.QueryRow(`SELECT version,(SELECT count(*) FROM pg_class WHERE relnamespace=current_schema()::regnamespace AND relname='budget_publication_operations') FROM budget_schema`).Scan(&version, &tables); err != nil || version != 19 || tables != 0 {
		t.Fatalf("partial v20 migration: %d %d %v", version, tables, err)
	}
	var after string
	if err = a.db.QueryRow(`SELECT jsonb_build_object('jobs',(SELECT jsonb_agg(to_jsonb(j)) FROM budget_jobs j),'commands',(SELECT jsonb_agg(to_jsonb(c)) FROM budget_operator_commands c),'reductions',(SELECT jsonb_agg(to_jsonb(m)) FROM budget_model_reductions m))::text`).Scan(&after); err != nil || after != before {
		t.Fatalf("failed migration changed authority: %v", err)
	}
	if _, err = a.db.Exec(`DROP TRIGGER fail_v20_commit ON budget_schema`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = Migrate(ctx, a.db); err != nil {
			t.Fatal(err)
		}
	}
	var operations int
	if err = a.db.QueryRow(`SELECT version,(SELECT count(*) FROM budget_publication_operations) FROM budget_schema`).Scan(&version, &operations); err != nil || version != 22 || operations != 0 {
		t.Fatalf("v20 invented publication operation: %d %d %v", version, operations, err)
	}
	if n, err := a.MaterializeAuthorizedPublications(ctx, 8); err != nil || n != 1 {
		t.Fatalf("migrated authorization cannot materialize: %d %v", n, err)
	}
	if op, err := a.LookupPublicationOperation(ctx, receipt.CommandID); err != nil || op.State != "pending" {
		t.Fatalf("migrated operation unavailable: %#v %v", op, err)
	}
}
