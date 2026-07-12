package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	// Register the SQLite SQL driver under the name "sqlite".
	_ "modernc.org/sqlite"
)

// newTestDB opens a file-backed SQLite database under t.TempDir(). A file DSN is
// required because ":memory:" yields a distinct database per pooled connection.
// MaxOpenConns is pinned to 1 to mirror the production SQLite pool and surface
// any self-deadlock in a step.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func ledgerVersions(t *testing.T, db *sql.DB) []int {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), "SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		t.Fatalf("query ledger: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var versions []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan ledger: %v", err)
		}
		versions = append(versions, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate ledger: %v", err)
	}
	return versions
}

func tableExistsT(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	ok, err := tableExists(context.Background(), db, SQLite, name)
	if err != nil {
		t.Fatalf("tableExists(%q): %v", name, err)
	}
	return ok
}

func ledgerName(t *testing.T, db *sql.DB, version int) (string, bool) {
	t.Helper()
	var name string
	err := db.QueryRowContext(context.Background(), "SELECT name FROM schema_migrations WHERE version = ?", version).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false
	}
	if err != nil {
		t.Fatalf("query ledger name: %v", err)
	}
	return name, true
}

func TestRun_FreshDatabase(t *testing.T) {
	db := newTestDB(t)
	steps := []Step{
		{Version: 1, Name: "create_users", SQL: "CREATE TABLE users (id INTEGER PRIMARY KEY)"},
		{Version: 2, Name: "create_orders", SQL: "CREATE TABLE orders (id INTEGER PRIMARY KEY)"},
	}

	if err := Run(context.Background(), db, SQLite, "", steps); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := ledgerVersions(t, db); !slices.Equal(got, []int{1, 2}) {
		t.Fatalf("ledger versions = %v, want [1 2]", got)
	}
	if name, ok := ledgerName(t, db, 1); !ok || name != "create_users" {
		t.Fatalf("ledger name for v1 = %q, ok=%v; want create_users", name, ok)
	}
	if !tableExistsT(t, db, "users") {
		t.Error("users table missing")
	}
	if !tableExistsT(t, db, "orders") {
		t.Error("orders table missing")
	}
}

func TestRun_Idempotent(t *testing.T) {
	db := newTestDB(t)
	steps := []Step{
		{Version: 1, Name: "create_marker", SQL: "CREATE TABLE marker (n INTEGER)"},
		{Version: 2, Name: "seed_marker", Fn: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO marker(n) VALUES (1)")
			return err
		}},
	}

	if err := Run(context.Background(), db, SQLite, "", steps); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if err := Run(context.Background(), db, SQLite, "", steps); err != nil {
		t.Fatalf("second Run: %v", err)
	}

	if got := ledgerVersions(t, db); !slices.Equal(got, []int{1, 2}) {
		t.Fatalf("ledger versions = %v, want [1 2]", got)
	}
	// The seed Fn must have run exactly once despite two Run calls.
	var count int
	if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM marker").Scan(&count); err != nil {
		t.Fatalf("count marker: %v", err)
	}
	if count != 1 {
		t.Fatalf("marker rows = %d, want 1 (step re-applied)", count)
	}
}

func TestRun_BaselineAdoption(t *testing.T) {
	db := newTestDB(t)

	// A pre-existing database already has the primary table.
	if _, err := db.ExecContext(context.Background(), "CREATE TABLE users (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatalf("pre-create users: %v", err)
	}

	steps := []Step{
		// Baseline SQL would fail if executed (users already exists), proving it
		// is stamped rather than run.
		{Version: 1, Name: "create_users", SQL: "CREATE TABLE users (id INTEGER PRIMARY KEY)"},
		{Version: 2, Name: "create_orders", SQL: "CREATE TABLE orders (id INTEGER PRIMARY KEY)"},
	}

	if err := Run(context.Background(), db, SQLite, "users", steps); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := ledgerVersions(t, db); !slices.Equal(got, []int{1, 2}) {
		t.Fatalf("ledger versions = %v, want [1 2]", got)
	}
	if !tableExistsT(t, db, "orders") {
		t.Error("later step did not apply: orders missing")
	}
}

func TestRun_PrimaryTableAbsent(t *testing.T) {
	db := newTestDB(t)

	steps := []Step{
		{Version: 1, Name: "create_users", SQL: "CREATE TABLE users (id INTEGER PRIMARY KEY)"},
		{Version: 2, Name: "create_orders", SQL: "CREATE TABLE orders (id INTEGER PRIMARY KEY)"},
	}

	// primaryTable is set but the table does not exist yet: baseline executes.
	if err := Run(context.Background(), db, SQLite, "users", steps); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := ledgerVersions(t, db); !slices.Equal(got, []int{1, 2}) {
		t.Fatalf("ledger versions = %v, want [1 2]", got)
	}
	if !tableExistsT(t, db, "users") {
		t.Error("baseline was not executed: users missing")
	}
}

func TestRun_FailureRollback(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	badSteps := []Step{
		{Version: 1, Name: "create_a", SQL: "CREATE TABLE a (id INTEGER PRIMARY KEY)"},
		// Creates b, then fails: the whole step must roll back.
		{Version: 2, Name: "broken", SQL: "CREATE TABLE b (id INTEGER); this is not valid sql;"},
	}

	if err := Run(ctx, db, SQLite, "", badSteps); err == nil {
		t.Fatal("Run should have failed on invalid SQL")
	}

	if got := ledgerVersions(t, db); !slices.Equal(got, []int{1}) {
		t.Fatalf("ledger versions = %v, want [1] (failed step not recorded)", got)
	}
	if !tableExistsT(t, db, "a") {
		t.Error("committed step 1 table a missing")
	}
	if tableExistsT(t, db, "b") {
		t.Error("failed step 2 DDL was not rolled back: b exists")
	}

	// Re-running retries the un-recorded version. With a corrected step it applies.
	goodSteps := []Step{
		badSteps[0],
		{Version: 2, Name: "create_b", SQL: "CREATE TABLE b (id INTEGER PRIMARY KEY)"},
	}
	if err := Run(ctx, db, SQLite, "", goodSteps); err != nil {
		t.Fatalf("retry Run: %v", err)
	}
	if got := ledgerVersions(t, db); !slices.Equal(got, []int{1, 2}) {
		t.Fatalf("ledger versions after retry = %v, want [1 2]", got)
	}
	if !tableExistsT(t, db, "b") {
		t.Error("retry did not create table b")
	}
}

func TestRun_FnStep(t *testing.T) {
	ctx := context.Background()

	t.Run("writes visible after commit", func(t *testing.T) {
		db := newTestDB(t)
		steps := []Step{
			{Version: 1, Name: "create_kv", SQL: "CREATE TABLE kv (k TEXT PRIMARY KEY, v TEXT)"},
			{Version: 2, Name: "seed_kv", Fn: func(ctx context.Context, tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, "INSERT INTO kv(k, v) VALUES ('a', 'b')")
				return err
			}},
		}
		if err := Run(ctx, db, SQLite, "", steps); err != nil {
			t.Fatalf("Run: %v", err)
		}
		var v string
		if err := db.QueryRowContext(ctx, "SELECT v FROM kv WHERE k = 'a'").Scan(&v); err != nil {
			t.Fatalf("read seeded row: %v", err)
		}
		if v != "b" {
			t.Fatalf("kv[a] = %q, want b", v)
		}
	})

	t.Run("error rolls back writes", func(t *testing.T) {
		db := newTestDB(t)
		wantErr := errors.New("boom")
		steps := []Step{
			{Version: 1, Name: "create_kv", SQL: "CREATE TABLE kv (k TEXT PRIMARY KEY, v TEXT)"},
			{Version: 2, Name: "half_seed", Fn: func(ctx context.Context, tx *sql.Tx) error {
				if _, err := tx.ExecContext(ctx, "INSERT INTO kv(k, v) VALUES ('a', 'b')"); err != nil {
					return err
				}
				return wantErr
			}},
		}
		err := Run(ctx, db, SQLite, "", steps)
		if !errors.Is(err, wantErr) {
			t.Fatalf("Run error = %v, want wrap of %v", err, wantErr)
		}
		if got := ledgerVersions(t, db); !slices.Equal(got, []int{1}) {
			t.Fatalf("ledger versions = %v, want [1]", got)
		}
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM kv").Scan(&count); err != nil {
			t.Fatalf("count kv: %v", err)
		}
		if count != 0 {
			t.Fatalf("kv rows = %d, want 0 (Fn write not rolled back)", count)
		}
	})
}

func TestRun_NoTxStep(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Sanity: VACUUM cannot run inside a transaction — the reason NoTx exists.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "VACUUM"); err == nil {
		t.Error("expected VACUUM inside a transaction to fail")
	}
	_ = tx.Rollback()

	steps := []Step{
		{Version: 1, Name: "create_t", SQL: "CREATE TABLE t (id INTEGER PRIMARY KEY)"},
		{Version: 2, Name: "vacuum", NoTx: func(ctx context.Context, db *sql.DB) error {
			_, err := db.ExecContext(ctx, "VACUUM")
			return err
		}},
	}
	if err := Run(ctx, db, SQLite, "", steps); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := ledgerVersions(t, db); !slices.Equal(got, []int{1, 2}) {
		t.Fatalf("ledger versions = %v, want [1 2]", got)
	}
}

func TestRun_Validation(t *testing.T) {
	noop := func(_ context.Context, _ *sql.Tx) error { return nil }
	noopDB := func(_ context.Context, _ *sql.DB) error { return nil }

	cases := []struct {
		name  string
		steps []Step
	}{
		{"zero steps", nil},
		{"duplicate versions", []Step{
			{Version: 1, Name: "a", SQL: "SELECT 1"},
			{Version: 1, Name: "b", SQL: "SELECT 1"},
		}},
		{"unordered versions", []Step{
			{Version: 2, Name: "a", SQL: "SELECT 1"},
			{Version: 1, Name: "b", SQL: "SELECT 1"},
		}},
		{"version below one", []Step{{Version: 0, Name: "a", SQL: "SELECT 1"}}},
		{"empty name", []Step{{Version: 1, Name: "", SQL: "SELECT 1"}}},
		{"both SQL and Fn", []Step{{Version: 1, Name: "a", SQL: "SELECT 1", Fn: noop}}},
		{"both SQL and NoTx", []Step{{Version: 1, Name: "a", SQL: "SELECT 1", NoTx: noopDB}}},
		{"both Fn and NoTx", []Step{{Version: 1, Name: "a", Fn: noop, NoTx: noopDB}}},
		{"all three", []Step{{Version: 1, Name: "a", SQL: "SELECT 1", Fn: noop, NoTx: noopDB}}},
		{"no work set", []Step{{Version: 1, Name: "a"}}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := newTestDB(t)
			if err := Run(context.Background(), db, SQLite, "", c.steps); err == nil {
				t.Fatalf("Run(%s) = nil, want validation error", c.name)
			}
		})
	}
}

// Every path below Run treats "not Postgres" as SQLite. An unrecognized dialect
// must be rejected rather than silently taking that path.
func TestRun_UnsupportedDialect(t *testing.T) {
	db := newTestDB(t)
	steps := []Step{{Version: 1, Name: "a", SQL: "CREATE TABLE a (id INTEGER)"}}

	err := Run(context.Background(), db, Dialect("mysql"), "", steps)
	if err == nil {
		t.Fatal("Run with an unsupported dialect returned nil")
	}
	if !strings.Contains(err.Error(), "unsupported dialect") {
		t.Fatalf("error = %v, want an unsupported-dialect error", err)
	}

	var tables int
	if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM sqlite_master WHERE type='table'").Scan(&tables); err != nil {
		t.Fatalf("count tables: %v", err)
	}
	if tables != 0 {
		t.Fatalf("Run created %d table(s) before rejecting the dialect", tables)
	}
}

// Rolling a deployment back leaves the schema at the newer shape. This build's
// statements were not written against it, so Run must stop rather than read and
// write rows through a schema it does not know.
func TestRun_RefusesDatabaseMigratedByNewerBuild(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	newer := []Step{
		{Version: 1, Name: "a", SQL: "CREATE TABLE a (id INTEGER)"},
		{Version: 2, Name: "b", SQL: "CREATE TABLE b (id INTEGER)"},
	}
	if err := Run(ctx, db, SQLite, "", newer); err != nil {
		t.Fatalf("seed newer schema: %v", err)
	}

	older := newer[:1]
	err := Run(ctx, db, SQLite, "", older)
	if err == nil {
		t.Fatal("Run against a forward-migrated database returned nil")
	}
	if !strings.Contains(err.Error(), "newer version") {
		t.Fatalf("error = %v, want a newer-version error", err)
	}
}

// ensureLedger and tableExists must both speak the Postgres dialect, even where
// no Postgres is available to run against: exercise the SQLite branches so the
// dialect switch itself is covered, and confirm the ledger DDL is idempotent.
func TestEnsureLedgerIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := ensureLedger(ctx, db, SQLite, defaultLedger); err != nil {
		t.Fatalf("first ensureLedger: %v", err)
	}
	if err := ensureLedger(ctx, db, SQLite, defaultLedger); err != nil {
		t.Fatalf("second ensureLedger: %v", err)
	}
	if !tableExistsT(t, db, defaultLedger) {
		t.Fatal("schema_migrations was not created")
	}
}

// RunNamed keeps each schema's versions in its own ledger, so two schemas can
// share one database without their version sequences colliding.
func TestRunNamed_SeparateLedgersDoNotCollide(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	keysSteps := []Step{{Version: 1, Name: "create_keys", SQL: "CREATE TABLE keys (id INTEGER PRIMARY KEY)"}}
	logsSteps := []Step{
		{Version: 1, Name: "create_logs", SQL: "CREATE TABLE logs (id INTEGER PRIMARY KEY)"},
		{Version: 2, Name: "index_logs", SQL: "CREATE INDEX idx_logs ON logs (id)"},
	}

	if err := RunNamed(ctx, db, SQLite, "keys_migrations", "keys", keysSteps); err != nil {
		t.Fatalf("run keys: %v", err)
	}
	// Sharing the same database, a second schema on its own ledger must not trip
	// checkNotAhead against the first schema's recorded versions.
	if err := RunNamed(ctx, db, SQLite, "logs_migrations", "logs", logsSteps); err != nil {
		t.Fatalf("run logs: %v", err)
	}

	if !tableExistsT(t, db, "keys") || !tableExistsT(t, db, "logs") {
		t.Fatal("expected both schemas to be created")
	}
	if !tableExistsT(t, db, "keys_migrations") || !tableExistsT(t, db, "logs_migrations") {
		t.Fatal("expected a separate ledger table per schema")
	}
}

func TestRunNamed_RequiresLedger(t *testing.T) {
	db := newTestDB(t)
	steps := []Step{{Version: 1, Name: "a", SQL: "CREATE TABLE a (id INTEGER)"}}
	if err := RunNamed(context.Background(), db, SQLite, "", "", steps); err == nil {
		t.Fatal("RunNamed with an empty ledger returned nil")
	}
}

// A NoTx step whose function fails is reported and not recorded, so it re-runs.
func TestRun_NoTxFailureIsNotRecorded(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	steps := []Step{
		{Version: 1, Name: "create", SQL: "CREATE TABLE t (id INTEGER)"},
		{Version: 2, Name: "broken_notx", NoTx: func(context.Context, *sql.DB) error {
			return errStub
		}},
	}
	if err := Run(ctx, db, SQLite, "", steps); !errors.Is(err, errStub) {
		t.Fatalf("Run error = %v, want it to wrap errStub", err)
	}
	if got := ledgerVersions(t, db); !slices.Equal(got, []int{1}) {
		t.Fatalf("ledger = %v, want [1]: the failed NoTx step was recorded", got)
	}
}

// A step returning ErrDeferStep is not recorded and does not abort Run: later
// steps still apply, and the deferred step retries on the next call.
func TestRun_DeferredStepNotRecordedAndRetried(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	deferIndex := true
	attempts := 0
	steps := []Step{
		{Version: 1, Name: "create_a", SQL: "CREATE TABLE a (id INTEGER PRIMARY KEY)"},
		{Version: 2, Name: "deferrable", NoTx: func(context.Context, *sql.DB) error {
			attempts++
			if deferIndex {
				return fmt.Errorf("transient build failure: %w", ErrDeferStep)
			}
			return nil
		}},
		{Version: 3, Name: "create_c", SQL: "CREATE TABLE c (id INTEGER PRIMARY KEY)"},
	}

	// First run: v2 defers. Run must not error, v2 must not be recorded, and the
	// independent later step v3 must still apply.
	if err := Run(ctx, db, SQLite, "", steps); err != nil {
		t.Fatalf("first Run returned error for a deferred step: %v", err)
	}
	if got := ledgerVersions(t, db); !slices.Equal(got, []int{1, 3}) {
		t.Fatalf("ledger = %v, want [1 3] (deferred v2 not recorded, v3 applied)", got)
	}
	if !tableExistsT(t, db, "c") {
		t.Error("later step v3 did not apply after v2 deferred")
	}

	// Second run: the deferred step retries. Now that it succeeds it is recorded.
	deferIndex = false
	if err := Run(ctx, db, SQLite, "", steps); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("deferred step ran %d times, want 2 (retried on second Run)", attempts)
	}
	if got := ledgerVersions(t, db); !slices.Equal(got, []int{1, 2, 3}) {
		t.Fatalf("ledger = %v, want [1 2 3] after the deferred step succeeds on retry", got)
	}
}

var errStub = errorString("stub failure")

type errorString string

func (e errorString) Error() string { return string(e) }
