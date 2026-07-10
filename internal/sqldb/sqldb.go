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
func Bind(dialect Dialect, query string) string {
	if dialect != Postgres {
		return query
	}
	var (
		b      strings.Builder
		argNum = 1
	)
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			fmt.Fprintf(&b, "$%d", argNum)
			argNum++
			continue
		}
		b.WriteByte(query[i])
	}
	return b.String()
}
