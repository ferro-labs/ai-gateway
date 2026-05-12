//go:build integration
// +build integration

package integration

import (
	"database/sql"
	"testing"

	_ "github.com/lib/pq"
)

var allowedTables = map[string]bool{
	"api_keys":     true,
	"request_logs": true,
	"config":       true,
}

func truncateTable(t *testing.T, table string) {
	t.Helper()
	if !allowedTables[table] {
		t.Fatalf("truncateTable: unknown table %q", table)
		return
	}
	db, err := sql.Open("postgres", testDSN)
	if err != nil {
		t.Fatalf("open db for truncate: %v", err)
	}
	defer func() { _ = db.Close() }()

	stmt, err := db.Prepare("DELETE FROM " + table) //nolint:gosec // table name is validated above
	if err != nil {
		t.Logf("prepare truncate %s (may not exist yet): %v", table, err)
		return
	}
	defer func() { _ = stmt.Close() }()
	if _, err := stmt.Exec(); err != nil {
		t.Logf("truncate %s: %v", table, err)
	}
}
