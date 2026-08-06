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
	path, err := filePath(dsn)
	if err != nil {
		return err
	}
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

// SplitQuery splits dsn on its first '?' into a base path/URI and the query
// string that follows it (without the leading '?'; "" if dsn has none). It is
// the same split filePath applies before resolving a path, exposed for
// callers that must derive a different DSN from this one while preserving
// any "?query" suffix (busy_timeout, journal_mode, ...) — see
// bootstrap.sessionDSN, which cannot silently drop it.
func SplitQuery(dsn string) (base, query string) {
	if i := strings.IndexByte(dsn, '?'); i >= 0 {
		return dsn[:i], dsn[i+1:]
	}
	return dsn, ""
}

// IsInMemory reports whether dsn resolves to an in-memory SQLite database
// with no backing file — the same resolution Secure applies via filePath (see
// its doc comment for the recognized forms). A dsn this cannot parse is
// reported as not in-memory rather than propagating the error, since the
// only current caller (bootstrap.sessionDSN) just needs to decide whether to
// derive a second DSN from this one; sql.Open surfaces the real parse error
// later when the DSN is actually opened.
func IsInMemory(dsn string) bool {
	path, err := filePath(dsn)
	return err == nil && path == ""
}

// filePath extracts the on-disk file path from a SQLite DSN. SQLite decodes
// percent escapes in file: URIs before opening the file, so this helper must
// resolve the same path before creating and restricting it.
func filePath(dsn string) (string, error) {
	path, query := SplitQuery(dsn)
	if strings.HasPrefix(path, "file:") {
		if q, err := url.ParseQuery(query); err == nil && q.Get("mode") == "memory" {
			return "", nil
		}

		parsed, err := url.Parse(path)
		if err != nil {
			return "", fmt.Errorf("parse sqlite file URI: %w", err)
		}

		switch {
		case parsed.Opaque != "":
			path, err = url.PathUnescape(parsed.Opaque)
			if err != nil {
				return "", fmt.Errorf("decode sqlite file URI path: %w", err)
			}
		case parsed.Host == "" || parsed.Host == "localhost":
			path = parsed.Path
		default:
			return "", fmt.Errorf("unsupported sqlite file URI authority %q", parsed.Host)
		}
	}

	if path == "" || path == ":memory:" {
		return "", nil
	}
	return filepath.Clean(path), nil
}
