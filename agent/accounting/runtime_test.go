package accounting

import (
	"context"
	"github.com/Y4NN777/7review/agent/review"
	"github.com/Y4NN777/7review/internal/testpostgres"
	"strings"
	"testing"
	"time"
)

func TestRuntimeSettingsCannotSilentlyEnablePartialAccounting(t *testing.T) {
	for _, values := range []map[string]string{{"ACCOUNTING_DATABASE_URL": "postgres://configured"}, {"ACCOUNTING_BOUNDARY_ID": "boundary"}, {"ACCOUNTING_PROJECT_ID": "project"}} {
		if _, err := ReadRuntimeSettings(func(key string) string { return values[key] }); err == nil {
			t.Fatal("partial shared accounting silently fell back")
		}
	}
	values := map[string]string{"ACCOUNTING_DATABASE_URL": "postgres://configured", "ACCOUNTING_BOUNDARY_ID": "boundary", "ACCOUNTING_PROJECT_ID": "project"}
	s, err := ReadRuntimeSettings(func(key string) string { return values[key] })
	if err != nil || !s.Enabled() {
		t.Fatalf("complete accounting settings not loaded: %#v %v", s, err)
	}
}

func TestPostgresRuntimeChecksSchemaWithoutAuthorizingDefaultRoutes(t *testing.T) {
	_, dsn := postgresFixture(t)
	limits := ledgerLimits()
	runtime, err := OpenRuntime(context.Background(), RuntimeSettings{dsn, "test-authority", "team"}, &limits)
	if err != nil || runtime == nil || runtime.Authority == nil {
		t.Fatalf("runtime not connected: %#v %v", runtime, err)
	}
	defer runtime.Close()
	if !runtime.Authority.cfg.RequireServiceJobs {
		t.Fatal("production runtime permits service intake outside durable jobs")
	}
	if _, err = runtime.Authority.AcceptInbox(context.Background(), review.Request{Provider: "github", ProjectID: "org/repo", ChangeID: "7"}, time.Minute); err == nil {
		t.Fatal("production runtime accepted incomplete aggregate through compatibility API")
	}
	if _, err = runtime.Authority.cfg.Catalog.Quote(quoteIntent(), boundsTime); err == nil {
		t.Fatal("enabling intake implicitly authorized an unqualified route")
	}
}

func TestRuntimeDatabaseErrorsNeverExposeCredentials(t *testing.T) {
	_, err := OpenDatabase(context.Background(), "postgres://operator:do-not-print-this@bad host/db")
	if err == nil || strings.Contains(err.Error(), "do-not-print-this") {
		t.Fatalf("unsafe database startup error: %v", err)
	}
}

func TestPostgresRuntimeNeverMigratesImplicitly(t *testing.T) {
	db, dsn := testpostgres.OpenDSN(t)
	limits := ledgerLimits()
	if runtime, err := OpenRuntime(context.Background(), RuntimeSettings{dsn, "test-authority", "team"}, &limits); err == nil || runtime != nil {
		if runtime != nil {
			runtime.Close()
		}
		t.Fatal("uninitialized database started accounting runtime")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM pg_class WHERE relnamespace=current_schema()::regnamespace AND left(relname,7)='budget_'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("runtime initialized an empty replacement store: %d %v", count, err)
	}
}
