//go:build integration
// +build integration

package integration

import (
	"context"
	"database/sql"
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
	_ "github.com/lib/pq"
)

var allowedTables = map[string]bool{
	"api_keys":       true,
	"config_history": true,
	"gateway_config": true,
	"request_logs":   true,
}

type testCloser interface {
	Close() error
}

func resetTablesAndClose(t *testing.T, closer testCloser, tables ...string) {
	t.Helper()
	for _, table := range tables {
		truncateTable(t, table)
	}
	t.Cleanup(func() {
		if err := closer.Close(); err != nil {
			t.Errorf("close test resource: %v", err)
		}
		for _, table := range tables {
			truncateTable(t, table)
		}
	})
}

func newTestGateway(t *testing.T, cfg aigateway.Config) (*aigateway.Gateway, error) {
	t.Helper()
	gw, err := aigateway.New(cfg)
	if err == nil {
		t.Cleanup(func() {
			if closeErr := gw.Close(); closeErr != nil {
				t.Errorf("close gateway: %v", closeErr)
			}
		})
	}
	return gw, err
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
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close truncate connection: %v", err)
		}
	}()

	if _, err := db.ExecContext(context.Background(), "DELETE FROM "+table); err != nil { //nolint:gosec // table name is validated above
		t.Fatalf("truncate %s: %v", table, err)
	}
}
