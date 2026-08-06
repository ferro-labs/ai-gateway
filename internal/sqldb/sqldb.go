// Package sqldb centralizes the SQL primitives shared by the gateway's
// persistence stores: the dialect type, the connection constructor (file
// hardening, pool tuning, ping), and placeholder binding. The API key, config,
// and request log stores all build on it so those primitives live in one place
// rather than being re-derived per store.
package sqldb

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/sqlitefile"

	// Register the Postgres SQL driver under the name "postgres".
	_ "github.com/lib/pq"
	// Register the SQLite SQL driver under the name "sqlite".
	_ "modernc.org/sqlite"
)

// Dialect selects the SQL flavor a store speaks. It is the single dialect type
// shared across the stores and the migration runner.
type Dialect string

// Supported dialects.
const (
	SQLite   Dialect = "sqlite"
	Postgres Dialect = "postgres"
)

// Connection-pool sizing. SQLite is pinned to a single connection because its
// file lock serializes writers and extra connections only add contention;
// Postgres keeps a small bounded pool with idle/lifetime caps so connections
// recycle rather than accumulate.
const (
	sqliteMaxOpenConns      = 1
	sqliteMaxIdleConns      = 1
	postgresMaxOpenConns    = 10
	postgresMaxIdleConns    = 5
	postgresConnMaxIdleTime = 5 * time.Minute
	postgresConnMaxLifetime = 30 * time.Minute
)

// sqliteBusyTimeout is how long a SQLite writer waits for the database lock
// before giving up. Five seconds is far longer than any statement here holds
// the lock and far shorter than a readiness probe's patience.
//
// Without it a blocked writer fails instantly with SQLITE_BUSY, and the one
// connection per pool above does not prevent that: it bounds each *pool*, not
// the file. Two stores pointed at one SQLite file — the configuration the
// separate migration ledgers exist to support, and the direct translation of
// the documented Postgres deployment where all three DSNs name one database —
// are two pools, two connections and two writers on one lock. Measured
// unset, half of the concurrent writes failed.
const sqliteBusyTimeout = 5 * time.Second

// sqliteBusyTimeoutName is the setting's name as it appears in a DSN.
//
// modernc.org/sqlite takes PRAGMA settings only as "_pragma=name(value)". A
// "_busy_timeout=5000" query parameter — the spelling several other SQLite
// drivers use — is accepted and ignored, so a DSN carrying one has no busy
// timeout at all while reading as though it does. Detection matches the bare
// name so either spelling counts as the operator having set it.
const sqliteBusyTimeoutName = "busy_timeout"

// Open opens a tuned, reachable database for the dialect.
//
// For SQLite a blank dsn falls back to defaultDSN, and the backing file is
// hardened to owner-only 0600 before the driver touches it: a file created
// under the process umask is world-readable until something narrows it, and the
// stores hold secrets. For Postgres a dsn is required and defaultDSN is unused.
//
// The connection pool is tuned for the dialect and the connection is verified
// with PingContext before returning. On any failure after sql.Open the
// half-open pool is closed so no descriptor leaks.
//
// # SQLite and concurrent writers
//
// SQLite locks the whole database file for a write, so every writer against one
// file is serialized whether it lives in this process or another. A default
// busy timeout is applied (see sqliteBusyTimeout) so a blocked writer waits its
// turn instead of failing immediately, which is what makes two stores sharing
// one file work.
//
// That makes contention survivable, not free. Waiting is still waiting, and
// nothing here coordinates schema migrations between processes: two gateway
// instances starting against one SQLite file race to apply them, and the loser
// waits out the timeout and then fails its startup. Run one gateway process per
// SQLite file. A multi-instance deployment belongs on Postgres, which is what
// every store's backend setting offers for exactly this reason.
func Open(ctx context.Context, dialect Dialect, dsn, defaultDSN string) (*sql.DB, error) {
	dsn = strings.TrimSpace(dsn)

	var driver string
	switch dialect {
	case SQLite:
		if dsn == "" {
			dsn = defaultDSN
		}
		if err := sqlitefile.Secure(dsn); err != nil {
			return nil, err
		}
		dsn = withBusyTimeout(dsn)
		driver = "sqlite"
	case Postgres:
		if dsn == "" {
			return nil, fmt.Errorf("postgres dsn is required")
		}
		driver = "postgres"
	default:
		return nil, fmt.Errorf("sqldb: unsupported dialect %q", dialect)
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s database: %w", dialect, err)
	}
	tune(db, dialect)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping %s database: %w", dialect, err)
	}
	return db, nil
}

// withBusyTimeout returns dsn carrying the default SQLite busy timeout, leaving
// it untouched when the operator already set one.
//
// The query is appended textually rather than through net/url, because a SQLite
// DSN is not always a URL: a bare path is the documented default and the common
// case, and round-tripping one through url.Parse would rewrite it.
func withBusyTimeout(dsn string) string {
	base, query := sqlitefile.SplitQuery(dsn)
	if strings.Contains(query, sqliteBusyTimeoutName) {
		return dsn
	}
	setting := fmt.Sprintf("_pragma=%s(%d)", sqliteBusyTimeoutName, sqliteBusyTimeout.Milliseconds())
	if query == "" {
		return base + "?" + setting
	}
	return base + "?" + query + "&" + setting
}

// tune applies the dialect's connection-pool settings.
func tune(db *sql.DB, dialect Dialect) {
	switch dialect {
	case SQLite:
		db.SetMaxOpenConns(sqliteMaxOpenConns)
		db.SetMaxIdleConns(sqliteMaxIdleConns)
		db.SetConnMaxIdleTime(0)
		db.SetConnMaxLifetime(0)
	case Postgres:
		db.SetMaxOpenConns(postgresMaxOpenConns)
		db.SetMaxIdleConns(postgresMaxIdleConns)
		db.SetConnMaxIdleTime(postgresConnMaxIdleTime)
		db.SetConnMaxLifetime(postgresConnMaxLifetime)
	}
}

// Bind rewrites '?' placeholders to Postgres '$N' form; SQLite keeps '?'. It is
// guarded on dialect so callers can route every statement through it
// unconditionally without renumbering SQLite queries.
//
// Only real placeholders are renumbered. A '?' inside a single-quoted string
// literal (with doubled single-quote escapes handled), a '--' line comment, or
// a '/* */' block comment is copied verbatim, so literal text survives intact.
// A bare '?' outside all of those is still treated as a placeholder — the
// Postgres jsonb '?' operator must be written as jsonb_exists(...) or a bound
// parameter, since it is indistinguishable from a placeholder without a full
// SQL parser.
func Bind(dialect Dialect, query string) string {
	if dialect != Postgres {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 8)
	argNum := 1
	i, n := 0, len(query)
	for i < n {
		switch c := query[i]; {
		case c == '\'':
			// Single-quoted literal: copy through the closing quote, treating a
			// doubled single-quote as an escaped quote, not the terminator.
			b.WriteByte(c)
			i++
			for i < n {
				b.WriteByte(query[i])
				if query[i] == '\'' {
					if i+1 < n && query[i+1] == '\'' {
						b.WriteByte(query[i+1])
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
		case c == '-' && i+1 < n && query[i+1] == '-':
			// Line comment: copy to (and including) the newline, or to the end.
			for i < n && query[i] != '\n' {
				b.WriteByte(query[i])
				i++
			}
		case c == '/' && i+1 < n && query[i+1] == '*':
			// Block comment: copy through the closing */.
			b.WriteString("/*")
			i += 2
			for i < n {
				if query[i] == '*' && i+1 < n && query[i+1] == '/' {
					b.WriteString("*/")
					i += 2
					break
				}
				b.WriteByte(query[i])
				i++
			}
		case c == '?':
			fmt.Fprintf(&b, "$%d", argNum)
			argNum++
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}
