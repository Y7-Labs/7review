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

func TestRuntimeSettingsDecodeExplicitRouteQualifications(t *testing.T) {
	raw := `{"schema_version":1,"routes":[{"provider":"qualified","model":"pinned-model-20261001","version":"contract-v1","currency":"USD","evidence_ref":"operator:provider-contract-1","valid_from":"2026-10-28T11:00:00Z","valid_until":"2026-10-28T13:00:00Z","input_hard_limit":100,"output_hard_limit":40,"input_micro_per_million":11,"output_micro_per_million":13,"fixed_micro":2,"all_output_bounded":true,"max_active_ms":50,"native_tools_qualified":false}]}`
	values := map[string]string{"ACCOUNTING_DATABASE_URL": "postgres://configured", "ACCOUNTING_BOUNDARY_ID": "boundary", "ACCOUNTING_PROJECT_ID": "project", "ACCOUNTING_ROUTE_BOUNDS_JSON": raw}
	settings, err := ReadRuntimeSettings(func(key string) string { return values[key] })
	if err != nil || len(settings.RouteBounds) != 1 || settings.RouteBounds[0].Provider != "qualified" {
		t.Fatalf("route qualifications not loaded: %#v %v", settings, err)
	}
	values["ACCOUNTING_ROUTE_BOUNDS_JSON"] = `{"schema_version":1,"routes":[]}`
	if _, err = ReadRuntimeSettings(func(key string) string { return values[key] }); err == nil {
		t.Fatal("invalid route qualifications accepted")
	}
	values = map[string]string{"ACCOUNTING_ROUTE_BOUNDS_JSON": raw}
	if _, err = ReadRuntimeSettings(func(key string) string { return values[key] }); err == nil {
		t.Fatal("route qualifications enabled without accounting authority")
	}
}

func TestPostgresRuntimeChecksSchemaWithoutAuthorizingDefaultRoutes(t *testing.T) {
	_, dsn := postgresFixture(t)
	limits := ledgerLimits()
	runtime, err := OpenRuntime(context.Background(), RuntimeSettings{DatabaseURL: dsn, BoundaryID: "test-authority", ProjectID: "team"}, &limits)
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

func TestPostgresRuntimeUsesOnlyExplicitRouteQualifications(t *testing.T) {
	_, dsn := postgresFixture(t)
	limits := ledgerLimits()
	runtime, err := OpenRuntime(context.Background(), RuntimeSettings{DatabaseURL: dsn, BoundaryID: "test-authority", ProjectID: "team", RouteBounds: []RouteBound{testRoute()}}, &limits)
	if err != nil || runtime == nil {
		t.Fatalf("qualified runtime unavailable: %#v %v", runtime, err)
	}
	defer runtime.Close()
	quote, err := runtime.Authority.cfg.Catalog.Quote(quoteIntent(), boundsTime)
	if err != nil || quote.RouteVersion != testRoute().Version || quote.EvidenceRef != testRoute().EvidenceRef {
		t.Fatalf("runtime changed explicit qualification: %#v %v", quote, err)
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
	if runtime, err := OpenRuntime(context.Background(), RuntimeSettings{DatabaseURL: dsn, BoundaryID: "test-authority", ProjectID: "team"}, &limits); err == nil || runtime != nil {
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
