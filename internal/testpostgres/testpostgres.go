// Package testpostgres creates isolated disposable schemas for integration tests.
package testpostgres

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func Open(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("SEVENREVIEW_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set SEVENREVIEW_TEST_POSTGRES for real PostgreSQL integration")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Host == "" {
		t.Fatal("test PostgreSQL requires a URL DSN")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	var value [8]byte
	if _, err = rand.Read(value[:]); err != nil {
		t.Fatal(err)
	}
	schema := "intake_test_" + hex.EncodeToString(value[:])
	if _, err = admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); _, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); admin.Close() })
	return db
}
