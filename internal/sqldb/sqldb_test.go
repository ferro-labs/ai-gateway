package sqldb

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
		{"postgres renumbers", Postgres, "SELECT * FROM t WHERE a = ? AND b = ?", "SELECT * FROM t WHERE a = $1 AND b = $2"},
		{"postgres no placeholders", Postgres, "SELECT 1", "SELECT 1"},
		{"postgres many", Postgres, "VALUES(?, ?, ?, ?)", "VALUES($1, $2, $3, $4)"},
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
