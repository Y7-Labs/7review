package accounting

import (
	"context"
	"testing"
)

func TestV19MigrationIsExplicitAtomicAndDoesNotInventPublicationAuthority(t *testing.T) {
	a, intent, actor, _, command, _ := publicationAuthorizationFixture(t)
	ctx := context.Background()
	snapshot := func() string {
		t.Helper()
		var raw string
		if err := a.db.QueryRow(`SELECT jsonb_build_object('jobs',(SELECT jsonb_agg(to_jsonb(x)) FROM (SELECT run_id,attempt_id,phase,dispatch_allowed,terminal_reduction_id,control_version,cancel_command_id FROM budget_jobs) x),'reductions',(SELECT jsonb_agg(to_jsonb(m)) FROM budget_model_reductions m),'inbox',(SELECT jsonb_agg(to_jsonb(i)) FROM budget_inbox i),'attempts',(SELECT jsonb_agg(to_jsonb(a)) FROM budget_attempts a))::text`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	before := snapshot()
	dropPublicationAuthorizationSchemaForMigration(t, a.db)
	if _, err := a.db.Exec(`UPDATE budget_schema SET version=18; CREATE FUNCTION fail_v19_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=19 THEN RAISE EXCEPTION 'injected v19 commit failure'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER fail_v19_commit AFTER UPDATE ON budget_schema DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_v19_commit()`); err != nil {
		t.Fatal(err)
	}
	if tx, err := a.begin(ctx); err == nil {
		tx.Rollback()
		t.Fatal("runtime accepted unmigrated v18")
	}
	if err := Migrate(ctx, a.db); err == nil {
		t.Fatal("failed v19 migration reported success")
	}
	var version, columns int
	if err := a.db.QueryRow(`SELECT version,(SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='budget_jobs' AND column_name='publication_authorization_id') FROM budget_schema`).Scan(&version, &columns); err != nil || version != 18 || columns != 0 || snapshot() != before {
		t.Fatalf("partial v19 migration: %d %d %v", version, columns, err)
	}
	if _, err := a.db.Exec(`DROP TRIGGER fail_v19_commit ON budget_schema`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(ctx, a.db); err != nil {
			t.Fatal(err)
		}
	}
	var inferred, commands int
	if err := a.db.QueryRow(`SELECT version,(SELECT count(*) FROM budget_jobs WHERE publication_authorization_id IS NOT NULL),(SELECT count(*) FROM budget_operator_commands) FROM budget_schema`).Scan(&version, &inferred, &commands); err != nil || version != 22 || inferred != 0 || commands != 0 || snapshot() != before {
		t.Fatalf("v19 invented authority: %d %d %d %v", version, inferred, commands, err)
	}
	if projection, err := a.LookupModelJob(ctx, intent.Job.RunID); err != nil || !projection.Succeeded || projection.Source.Report.Draft == "" {
		t.Fatalf("migration damaged terminal assessment: %+v %v", projection, err)
	}
	if receipt, err := a.AuthorizePublication(ctx, actor, command); err != nil || receipt.State != "authorized" {
		t.Fatalf("migrated assessment cannot be authorized: %+v %v", receipt, err)
	}
}
