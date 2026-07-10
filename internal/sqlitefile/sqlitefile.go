// Package sqlitefile hardens on-disk SQLite database file permissions.
package sqlitefile

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Secure creates the SQLite database file if it does not exist and restricts it
// to owner-only read/write (0600). Call it *before* opening the database.
//
// SQLite creates missing files honoring the process umask, which can leave them
// world-readable. Chmod-ing afterwards is not enough: a process that opened the
// file during that window keeps its descriptor, and reads everything written
// later. Creating the file ourselves closes the window. It also means SQLite's
// rollback journal and write-ahead log inherit 0600, since SQLite gives those
// the mode of the database file.
//
// dsn is the same string passed to sql.Open("sqlite", dsn) — a bare file
// path, or a "file:"/"file://" URI with an optional "?query" suffix (see
// modernc.org/sqlite's conn.go newConn). In-memory DSNs (":memory:", a
// "file:" URI whose path is ":memory:", or any URI with a "mode=memory"
// query parameter — including named shared-cache in-memory databases like
// "file:mydb?mode=memory&cache=shared") have no backing file and are a no-op.
func Secure(dsn string) error {
	path := filePath(dsn)
	if path == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // G304: path is the operator-supplied DSN.
	if err != nil {
		return fmt.Errorf("create sqlite file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("create sqlite file: %w", err)
	}
	// An existing file keeps whatever mode it already had, and the umask can
	// strip bits from the mode above, so restrict it explicitly.
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("restrict sqlite file permissions: %w", err)
	}
	return nil
}

// filePath extracts the on-disk file path from a SQLite DSN: it strips any
// "?query" suffix and a leading "file://" or "file:" scheme, returning "" for
// in-memory databases (":memory:", or any DSN whose query string sets
// "mode=memory") that have no file to secure.
func filePath(dsn string) string {
	path := dsn
	query := ""
	if i := strings.IndexByte(path, '?'); i >= 0 {
		query = path[i+1:]
		path = path[:i]
	}
	path = strings.TrimPrefix(path, "file://")
	path = strings.TrimPrefix(path, "file:")
	if path == "" || path == ":memory:" {
		return ""
	}
	if q, err := url.ParseQuery(query); err == nil && q.Get("mode") == "memory" {
		return ""
	}
	return filepath.Clean(path)
}
