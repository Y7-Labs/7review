package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/Y4NN777/7review/agent/accounting"
	"github.com/Y4NN777/7review/internal/testpostgres"
)

func TestAccountingMigrationCommandRefusesImplicitOrMissingDatabase(t *testing.T) {
	t.Setenv("ACCOUNTING_DATABASE_URL", "")
	for _, args := range [][]string{nil, {"review"}, {"migrate"}} {
		if err := runAccountingCommand(args, new(bytes.Buffer)); err == nil {
			t.Fatalf("migration accepted missing/invalid authority: %v", args)
		}
	}
}
func TestAccountingMigrationCommandUsesExplicitDatabase(t *testing.T) {
	db, dsn := testpostgres.OpenDSN(t)
	t.Setenv("ACCOUNTING_DATABASE_URL", dsn)
	var out bytes.Buffer
	if err := runAccountingCommand([]string{"migrate"}, &out); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRow(`SELECT version FROM budget_schema`).Scan(&version); err != nil || version != 17 {
		t.Fatalf("migration command did not initialize current schema: %d %v", version, err)
	}
	if err := accounting.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
}
