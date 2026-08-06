package requestlog

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/sqldb"
)

// sqliteColumnType returns the type request_logs declares for column, as
// pragma_table_info reports it.
func sqliteColumnType(t *testing.T, db *sql.DB, column string) string {
	t.Helper()
	var declared string
	if err := db.QueryRowContext(context.Background(),
		"SELECT type FROM pragma_table_info('request_logs') WHERE name = ?", column).Scan(&declared); err != nil {
		t.Fatalf("probe request_logs.%s type: %v", column, err)
	}
	return declared
}

// TestSQLiteWriter_AdoptsMigratedDatabaseWithoutLedger covers a request_logs
// table that already carries the timing and cost columns but has no ledger to
// say so — a restore that omitted the ledger table, say. RunNamed adopts such a
// database at the baseline and then replays every later step, so version 3 has
// to tolerate columns that are already there instead of wedging every start on a
// duplicate column.
func TestSQLiteWriter_AdoptsMigratedDatabaseWithoutLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledgerless-requests.db")

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	if _, err := raw.ExecContext(context.Background(), `
CREATE TABLE request_logs (
	id INTEGER PRIMARY KEY,
	trace_id TEXT,
	stage TEXT NOT NULL,
	model TEXT,
	provider TEXT,
	prompt_tokens INTEGER NOT NULL,
	completion_tokens INTEGER NOT NULL,
	total_tokens INTEGER NOT NULL,
	error_message TEXT,
	created_at TIMESTAMP NOT NULL,
	duration_ms DOUBLE PRECISION,
	ttft_ms DOUBLE PRECISION,
	cost_usd DOUBLE PRECISION
)`); err != nil {
		t.Fatalf("create migrated table: %v", err)
	}
	if _, err := raw.ExecContext(context.Background(),
		`INSERT INTO request_logs(trace_id, stage, model, provider, prompt_tokens, completion_tokens, total_tokens, error_message, created_at, duration_ms, ttft_ms, cost_usd)
		VALUES('ledgerless-trace', 'after_request', 'gpt-4', 'openai', 1, 2, 3, '', ?, 12.5, 4.5, 0.25)`, time.Now().UTC()); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}

	w, err := NewSQLiteWriter(t.Context(), path)
	if err != nil {
		t.Fatalf("open writer on a migrated database with no ledger: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	result, err := w.List(context.Background(), Query{Limit: 10})
	if err != nil {
		t.Fatalf("list logs: %v", err)
	}
	if result.Total != 1 || len(result.Data) != 1 || result.Data[0].CostUSD == nil || *result.Data[0].CostUSD != 0.25 {
		t.Fatalf("expected the pre-existing row with its cost, got total=%d data=%+v", result.Total, result.Data)
	}
}

// TestTimingAndCostColumnsAreDoublePrecision pins the type the three
// measurement columns are created with. SQLite stores a REAL as an 8-byte
// double whatever the declared type says, so the check is on the declared type:
// it is the same constant both dialects are built from, and on Postgres REAL
// would be a float4 carrying about seven significant digits — enough to round a
// per-request cost that SQLite keeps exactly.
func TestTimingAndCostColumnsAreDoublePrecision(t *testing.T) {
	w, err := NewSQLiteWriter(t.Context(), filepath.Join(t.TempDir(), "requests.db"))
	if err != nil {
		t.Fatalf("new sqlite writer: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	// Spelled out rather than compared against doubleType, which would pass for
	// whatever that constant happens to say.
	for _, column := range timingAndCostColumns {
		if declared := sqliteColumnType(t, w.db, column); declared != "DOUBLE PRECISION" {
			t.Errorf("request_logs.%s is declared %s, want DOUBLE PRECISION", column, declared)
		}
	}
}

// TestWidenStatements covers the Postgres half of the same parity, which no
// SQLite test can reach: a database whose version 3 created the columns as
// float4 keeps them until they are altered, and SQLite has nothing to alter.
func TestWidenStatements(t *testing.T) {
	if got := widenStatements(sqldb.SQLite); got != nil {
		t.Errorf("sqlite needs no widening, got %v", got)
	}

	want := []string{
		"ALTER TABLE request_logs ALTER COLUMN duration_ms TYPE DOUBLE PRECISION",
		"ALTER TABLE request_logs ALTER COLUMN ttft_ms TYPE DOUBLE PRECISION",
		"ALTER TABLE request_logs ALTER COLUMN cost_usd TYPE DOUBLE PRECISION",
	}
	if got := widenStatements(sqldb.Postgres); !reflect.DeepEqual(got, want) {
		t.Errorf("postgres widen statements:\n got %v\nwant %v", got, want)
	}
}
