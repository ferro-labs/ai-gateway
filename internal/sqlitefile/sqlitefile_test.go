package sqlitefile

import (
	"net/url"
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

// Securing a file that SQLite has not created yet is the point: a file created
// under the process umask is world-readable, and a process that opened it in
// that window keeps reading everything written afterwards.
func TestSecure_CreatesMissingFileRestricted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")

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
	if info.Size() != 0 {
		t.Errorf("expected an empty file, got %d bytes", info.Size())
	}
}

// SQLite reads a zero-byte file as an empty database, so pre-creating one must
// not disturb a database opened over it afterwards.
func TestSecure_IsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "twice.db")
	if err := Secure(path); err != nil {
		t.Fatalf("first Secure: %v", err)
	}
	if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	if err := Secure(path); err != nil {
		t.Fatalf("second Secure: %v", err)
	}

	raw, err := os.ReadFile(path) //nolint:gosec // path is from t.TempDir()
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(raw) != "payload" {
		t.Fatalf("Secure truncated an existing database: %q", raw)
	}
}

func TestSecure_UncreatableFileReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-dir", "test.db")
	if err := Secure(path); err == nil {
		t.Fatal("expected an error for a path whose directory does not exist")
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

	t.Run("bare path with mode query remains on disk", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "test.db")

		if err := Secure(path + "?mode=memory"); err != nil {
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

func TestSecure_PercentEncodedFileURI(t *testing.T) {
	dir := t.TempDir()
	encodedPath := filepath.Join(dir, "encoded%20name.db")
	actualPath := filepath.Join(dir, "encoded name.db")

	dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(actualPath)}).String()
	if err := Secure(dsn); err != nil {
		t.Fatalf("Secure: %v", err)
	}

	info, err := os.Stat(actualPath)
	if err != nil {
		t.Fatalf("stat decoded path: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("expected mode 0600, got %o", perm)
	}
	if _, err := os.Stat(encodedPath); !os.IsNotExist(err) {
		t.Fatalf("encoded path should not have been created: %v", err)
	}
}

func TestSecure_LocalhostFileURI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "localhost.db")
	dsn := (&url.URL{Scheme: "file", Host: "localhost", Path: filepath.ToSlash(path)}).String()
	if err := Secure(dsn); err != nil {
		t.Fatalf("Secure: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat localhost URI path: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("expected mode 0600, got %o", perm)
	}
}

func TestSecure_InvalidFileURI(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "bad%zz.db")
	if err := Secure(dsn); err == nil {
		t.Fatal("expected an invalid percent escape to fail")
	}
}
