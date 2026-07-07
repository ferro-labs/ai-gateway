// Package sqlitefile hardens on-disk SQLite database file permissions.
package sqlitefile

import (
	"fmt"
	"os"
	"strings"
)

// Secure restricts a SQLite database file to owner-only read/write (0600).
// SQLite creates new files honoring the process umask, which can leave them
// world-readable; call this immediately after the file is known to exist
// (e.g. after a successful Ping()).
//
// dsn is the same string passed to sql.Open("sqlite", dsn) — a bare file
// path, or a "file:"/"file://" URI with an optional "?query" suffix (see
// modernc.org/sqlite's conn.go newConn). In-memory DSNs (":memory:" or a
// "file:" URI whose path is ":memory:") have no backing file and are a no-op.
func Secure(dsn string) error {
	path := filePath(dsn)
	if path == "" {
		return nil
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("restrict sqlite file permissions: %w", err)
	}
	return nil
}

// filePath extracts the on-disk file path from a SQLite DSN: it strips any
// "?query" suffix and a leading "file://" or "file:" scheme, returning "" for
// in-memory databases that have no file to secure.
func filePath(dsn string) string {
	path := dsn
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	path = strings.TrimPrefix(path, "file://")
	path = strings.TrimPrefix(path, "file:")
	if path == "" || path == ":memory:" {
		return ""
	}
	return path
}
