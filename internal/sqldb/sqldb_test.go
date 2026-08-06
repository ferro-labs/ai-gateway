package sqldb

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestBind(t *testing.T) {
	cases := []struct {
		name    string
		dialect Dialect
		query   string
		want    string
	}{
		{"sqlite keeps placeholders", SQLite, "SELECT * FROM t WHERE a = ? AND b = ?", "SELECT * FROM t WHERE a = ? AND b = ?"},
		{"sqlite keeps literal untouched", SQLite, "SELECT * FROM t WHERE note = 'is it? yes' AND a = ?", "SELECT * FROM t WHERE note = 'is it? yes' AND a = ?"},
		{"postgres renumbers", Postgres, "SELECT * FROM t WHERE a = ? AND b = ?", "SELECT * FROM t WHERE a = $1 AND b = $2"},
		{"postgres no placeholders", Postgres, "SELECT 1", "SELECT 1"},
		{"postgres many", Postgres, "VALUES(?, ?, ?, ?)", "VALUES($1, $2, $3, $4)"},
		{"postgres skips question mark in literal", Postgres, "SELECT * FROM t WHERE note = 'is it? yes' AND a = ?", "SELECT * FROM t WHERE note = 'is it? yes' AND a = $1"},
		{"postgres skips question mark in doubled-quote literal", Postgres, "INSERT INTO t(a, b) VALUES(?, 'it''s a ? mark')", "INSERT INTO t(a, b) VALUES($1, 'it''s a ? mark')"},
		{"postgres literal does not consume arg number", Postgres, "INSERT INTO t(a, b, c) VALUES(?, 'x?y', ?)", "INSERT INTO t(a, b, c) VALUES($1, 'x?y', $2)"},
		{"postgres skips question mark in line comment", Postgres, "SELECT ? -- trailing? comment\nWHERE b = ?", "SELECT $1 -- trailing? comment\nWHERE b = $2"},
		{"postgres skips question mark in block comment", Postgres, "SELECT ? /* huh? */ , ?", "SELECT $1 /* huh? */ , $2"},
		{"postgres empty literal then placeholder", Postgres, "SELECT '', ?", "SELECT '', $1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Bind(tc.dialect, tc.query); got != tc.want {
				t.Fatalf("Bind(%s) = %q, want %q", tc.dialect, got, tc.want)
			}
		})
	}
}

func TestOpen_SQLiteSecuresFileAndPings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "open.db")
	db, err := Open(context.Background(), SQLite, path, "unused-default.db")
	if err != nil {
		t.Fatalf("Open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat sqlite file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("expected file mode 0600, got %o", perm)
	}
}

func TestOpen_SQLiteBlankDSNUsesDefault(t *testing.T) {
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	db, err := Open(context.Background(), SQLite, "   ", "default.db")
	if err != nil {
		t.Fatalf("Open with blank dsn: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := os.Stat("default.db"); err != nil {
		t.Fatalf("expected default dsn file to be created: %v", err)
	}
}

func TestOpen_PostgresRequiresDSN(t *testing.T) {
	_, err := Open(context.Background(), Postgres, "   ", "")
	if err == nil || !strings.Contains(err.Error(), "postgres dsn is required") {
		t.Fatalf("expected postgres dsn required error, got %v", err)
	}
}

func TestOpen_UnsupportedDialect(t *testing.T) {
	_, err := Open(context.Background(), Dialect("mysql"), "dsn", "")
	if err == nil || !strings.Contains(err.Error(), "unsupported dialect") {
		t.Fatalf("expected unsupported dialect error, got %v", err)
	}
}

// TestWithBusyTimeout covers the DSN shapes an operator can supply: a bare
// path, a path that already carries a query, and one that already sets the
// timeout under either driver's spelling.
func TestWithBusyTimeout(t *testing.T) {
	cases := []struct {
		name string
		dsn  string
		want string
	}{
		{"bare path gains the setting", "/data/ferrogw.db", "/data/ferrogw.db?_pragma=busy_timeout(5000)"},
		{"existing query is preserved", "/data/ferrogw.db?_txlock=immediate", "/data/ferrogw.db?_txlock=immediate&_pragma=busy_timeout(5000)"},
		{"file URI gains the setting", "file:/data/ferrogw.db", "file:/data/ferrogw.db?_pragma=busy_timeout(5000)"},
		{"operator pragma wins", "/data/ferrogw.db?_pragma=busy_timeout(60000)", "/data/ferrogw.db?_pragma=busy_timeout(60000)"},
		{"operator _busy_timeout spelling wins", "/data/ferrogw.db?_busy_timeout=60000", "/data/ferrogw.db?_busy_timeout=60000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := withBusyTimeout(tc.dsn); got != tc.want {
				t.Fatalf("withBusyTimeout(%q) = %q, want %q", tc.dsn, got, tc.want)
			}
		})
	}
}

// TestOpen_SQLiteSharedFileWritersWait is the regression for the topology the
// separate migration ledgers exist to support: two stores in ONE process
// pointed at one SQLite file. Each pool is capped at one connection, which
// bounds the pool and not the file, so the two are two writers on one lock.
//
// Without a busy timeout a blocked writer fails instantly with SQLITE_BUSY —
// measured at half of the writes below — rather than waiting the microseconds
// the other transaction needs.
func TestOpen_SQLiteSharedFileWritersWait(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "shared.db")
	ctx := context.Background()

	first, err := Open(ctx, SQLite, dsn, "")
	if err != nil {
		t.Fatalf("open first: %v", err)
	}
	defer func() { _ = first.Close() }()
	second, err := Open(ctx, SQLite, dsn, "")
	if err != nil {
		t.Fatalf("open second: %v", err)
	}
	defer func() { _ = second.Close() }()

	if _, err := first.ExecContext(ctx, "CREATE TABLE t(id INTEGER PRIMARY KEY, v TEXT)"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	const writesPerStore = 50
	var wg sync.WaitGroup
	failures := make(chan error, 2*writesPerStore)
	for _, db := range []*sql.DB{first, second} {
		for range writesPerStore {
			wg.Add(1)
			go func(db *sql.DB) {
				defer wg.Done()
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					failures <- err
					return
				}
				if _, err := tx.ExecContext(ctx, "INSERT INTO t(v) VALUES('x')"); err != nil {
					failures <- err
					_ = tx.Rollback()
					return
				}
				if err := tx.Commit(); err != nil {
					failures <- err
				}
			}(db)
		}
	}
	wg.Wait()
	close(failures)

	count := 0
	for err := range failures {
		if count == 0 {
			t.Errorf("write against a shared SQLite file failed instead of waiting: %v", err)
		}
		count++
	}
	if count > 0 {
		t.Fatalf("%d of %d writes failed", count, 2*writesPerStore)
	}
}
