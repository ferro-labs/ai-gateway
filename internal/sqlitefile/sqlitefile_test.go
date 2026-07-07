package sqlitefile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSecure_RestrictsPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	// os.Create honors the process umask (typically leaving the file
	// world-readable), mirroring how SQLite creates its database file.
	f, err := os.Create(path) //nolint:gosec // path is from t.TempDir()
	if err != nil {
		t.Fatalf("create test file: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close test file: %v", err)
	}

	if err := Secure(path); err != nil {
		t.Fatalf("Secure: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("expected mode 0600, got %o", perm)
	}
}

func TestSecure_MissingFileReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	if err := Secure(path); err == nil {
		t.Fatal("expected error for a file that does not exist")
	}
}

// TestSecure_DSNVariants covers the modernc.org/sqlite DSN forms documented in
// its driver.go ("file:///tmp/mydata.sqlite?_pragma=foreign_keys(1)&..."):
// a bare path, a bare path with query params (no "file:" scheme), and
// "file:"/"file://" URIs with query params. See conn.go's newConn, which
// splits everything from "?" onward as query params.
func TestSecure_DSNVariants(t *testing.T) {
	newFile := func(t *testing.T, path string) {
		t.Helper()
		f, err := os.Create(path) //nolint:gosec // path is from t.TempDir()
		if err != nil {
			t.Fatalf("create test file: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("close test file: %v", err)
		}
	}

	t.Run("bare path with query params", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "test.db")
		newFile(t, path)

		if err := Secure(path + "?_pragma=busy_timeout(5000)"); err != nil {
			t.Fatalf("Secure: %v", err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("expected mode 0600, got %o", perm)
		}
	})

	t.Run("file scheme with query params", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "test.db")
		newFile(t, path)

		if err := Secure("file://" + path + "?_pragma=busy_timeout(5000)&cache=shared"); err != nil {
			t.Fatalf("Secure: %v", err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("expected mode 0600, got %o", perm)
		}
	})

	t.Run("in-memory DSNs are a no-op", func(t *testing.T) {
		// file:name?mode=memory[&cache=shared] is SQLite's named in-memory
		// database form: "name" is a cache-sharing key, not a real file, even
		// though it isn't the literal ":memory:" string.
		for _, dsn := range []string{
			":memory:",
			"file::memory:?cache=shared",
			"file:test.db?mode=memory",
			"file:my_shared_db?mode=memory&cache=shared",
			"file:my_shared_db?cache=shared&mode=memory",
		} {
			if err := Secure(dsn); err != nil {
				t.Errorf("Secure(%q): expected no-op for in-memory DSN, got error: %v", dsn, err)
			}
		}
	})
}
