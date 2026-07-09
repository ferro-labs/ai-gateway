package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/ferro-labs/ai-gateway/internal/migrations"
)

// keyRowColumns is the column list shared by the rebuild's INSERT and SELECT.
const keyRowColumns = "id, key_hash, key_display, name, scopes, created_at, revoked_at, expires_at, rotated_at, active, usage_count, last_used_at"

// keyStoreSteps returns the migration sequence for the api_keys database.
//
// Version 1 is the pre-1.1.21 schema, including the two columns that earlier
// releases added with a bare ALTER TABLE. Databases created before the runner
// existed already have this shape, so Run adopts it as a baseline rather than
// executing it.
//
// Version 2 replaces the plaintext key column with its SHA-256 hash and a
// display form. Version 3 erases the pages the rebuild freed.
func keyStoreSteps(dialect migrations.Dialect) []migrations.Step {
	return []migrations.Step{
		{Version: 1, Name: "api_keys_baseline", SQL: baselineDDL(dialect)},
		{Version: 2, Name: "api_keys_hash", Fn: hashStoredKeys(dialect)},
		{Version: 3, Name: "api_keys_scrub", NoTx: scrubFreedPages(dialect)},
	}
}

func baselineDDL(dialect migrations.Dialect) string {
	if dialect == migrations.Postgres {
		return `
CREATE TABLE IF NOT EXISTS api_keys (
	id TEXT PRIMARY KEY,
	key TEXT UNIQUE NOT NULL,
	name TEXT NOT NULL,
	scopes TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL,
	revoked_at TIMESTAMPTZ NULL,
	expires_at TIMESTAMPTZ NULL,
	rotated_at TIMESTAMPTZ NULL,
	active BOOLEAN NOT NULL,
	usage_count INTEGER NOT NULL DEFAULT 0,
	last_used_at TIMESTAMPTZ NULL
)`
	}
	return `
CREATE TABLE IF NOT EXISTS api_keys (
	id TEXT PRIMARY KEY,
	key TEXT UNIQUE NOT NULL,
	name TEXT NOT NULL,
	scopes TEXT NOT NULL,
	created_at DATETIME NOT NULL,
	revoked_at DATETIME NULL,
	expires_at DATETIME NULL,
	rotated_at DATETIME NULL,
	active BOOLEAN NOT NULL,
	usage_count INTEGER NOT NULL DEFAULT 0,
	last_used_at DATETIME NULL
)`
}

func hashedTableDDL(dialect migrations.Dialect) string {
	if dialect == migrations.Postgres {
		return `
CREATE TABLE api_keys_new (
	id TEXT PRIMARY KEY,
	key_hash TEXT UNIQUE NOT NULL,
	key_display TEXT NOT NULL,
	name TEXT NOT NULL,
	scopes TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL,
	revoked_at TIMESTAMPTZ NULL,
	expires_at TIMESTAMPTZ NULL,
	rotated_at TIMESTAMPTZ NULL,
	active BOOLEAN NOT NULL,
	usage_count INTEGER NOT NULL DEFAULT 0,
	last_used_at TIMESTAMPTZ NULL
)`
	}
	return `
CREATE TABLE api_keys_new (
	id TEXT PRIMARY KEY,
	key_hash TEXT UNIQUE NOT NULL,
	key_display TEXT NOT NULL,
	name TEXT NOT NULL,
	scopes TEXT NOT NULL,
	created_at DATETIME NOT NULL,
	revoked_at DATETIME NULL,
	expires_at DATETIME NULL,
	rotated_at DATETIME NULL,
	active BOOLEAN NOT NULL,
	usage_count INTEGER NOT NULL DEFAULT 0,
	last_used_at DATETIME NULL
)`
}

// hashStoredKeys replaces the plaintext key column with sha256(key) plus a
// display form, by rebuilding the table.
//
// The rebuild is not optional. SQLite refuses to DROP a UNIQUE column, and on
// Postgres DROP COLUMN only edits the catalog — the plaintext stays in the heap
// until the rows are rewritten. Creating a new table and dropping the old one
// satisfies both: SQLite gets its rebuild, Postgres gets a fresh relfilenode
// and unlinks the file holding the secrets.
func hashStoredKeys(dialect migrations.Dialect) func(context.Context, *sql.Tx) error {
	return func(ctx context.Context, tx *sql.Tx) error {
		// Concurrent writers would otherwise be lost between the copy and the
		// rename. SQLite serializes writers already.
		if dialect == migrations.Postgres {
			if _, err := tx.ExecContext(ctx, "LOCK TABLE api_keys IN ACCESS EXCLUSIVE MODE"); err != nil {
				return fmt.Errorf("lock api_keys: %w", err)
			}
		}

		// A database last opened by a release that predates the usage columns
		// is adopted at the baseline without them, so the rebuild's SELECT
		// would fail. Add whatever is missing before reading.
		for _, col := range baselineUsageColumns(dialect) {
			if err := ensureColumn(ctx, tx, dialect, "api_keys", col.name, col.ddl); err != nil {
				return err
			}
		}

		if err := addHashColumns(ctx, tx); err != nil {
			return err
		}

		secrets, err := readPlaintextKeys(ctx, tx)
		if err != nil {
			return err
		}

		update := bindPlaceholders(dialect, "UPDATE api_keys SET key_hash = ?, key_display = ? WHERE id = ?")
		for _, s := range secrets {
			if _, err := tx.ExecContext(ctx, update, hashKey(s.key), displayKey(s.key), s.id); err != nil {
				return fmt.Errorf("hash stored key: %w", err)
			}
		}

		if _, err := tx.ExecContext(ctx, hashedTableDDL(dialect)); err != nil {
			return fmt.Errorf("create hashed api_keys table: %w", err)
		}
		copyRows := fmt.Sprintf("INSERT INTO api_keys_new (%s) SELECT %s FROM api_keys", keyRowColumns, keyRowColumns)
		if _, err := tx.ExecContext(ctx, copyRows); err != nil { //nolint:gosec // G202: keyRowColumns is a package constant, not input.
			return fmt.Errorf("copy api_keys rows: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "DROP TABLE api_keys"); err != nil {
			return fmt.Errorf("drop plaintext api_keys table: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "ALTER TABLE api_keys_new RENAME TO api_keys"); err != nil {
			return fmt.Errorf("rename hashed api_keys table: %w", err)
		}
		return nil
	}
}

type storedSecret struct {
	id  string
	key string
}

// readPlaintextKeys loads every row before any write is issued. The SQLite pool
// runs with a single connection, so holding this cursor open across an Exec on
// the same transaction would deadlock.
func readPlaintextKeys(ctx context.Context, tx *sql.Tx) ([]storedSecret, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id, key FROM api_keys")
	if err != nil {
		return nil, fmt.Errorf("read stored keys: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var secrets []storedSecret
	for rows.Next() {
		var s storedSecret
		if err := rows.Scan(&s.id, &s.key); err != nil {
			return nil, fmt.Errorf("scan stored key: %w", err)
		}
		secrets = append(secrets, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stored keys: %w", err)
	}
	return secrets, nil
}

func addHashColumns(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		"ALTER TABLE api_keys ADD COLUMN key_hash TEXT",
		"ALTER TABLE api_keys ADD COLUMN key_display TEXT",
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("add hash columns: %w", err)
		}
	}
	return nil
}

type columnSpec struct {
	name string
	ddl  string
}

func baselineUsageColumns(dialect migrations.Dialect) []columnSpec {
	lastUsed := "ALTER TABLE api_keys ADD COLUMN last_used_at DATETIME NULL"
	if dialect == migrations.Postgres {
		lastUsed = "ALTER TABLE api_keys ADD COLUMN last_used_at TIMESTAMPTZ NULL"
	}
	return []columnSpec{
		{name: "usage_count", ddl: "ALTER TABLE api_keys ADD COLUMN usage_count INTEGER NOT NULL DEFAULT 0"},
		{name: "last_used_at", ddl: lastUsed},
	}
}

// ensureColumn adds a column when the table does not already have it. It probes
// the catalog rather than executing the ALTER and matching the resulting error
// message.
func ensureColumn(ctx context.Context, tx *sql.Tx, dialect migrations.Dialect, table, column, ddl string) error {
	probe := "SELECT 1 FROM pragma_table_info(?) WHERE name = ?"
	if dialect == migrations.Postgres {
		probe = "SELECT 1 FROM information_schema.columns WHERE table_name = $1 AND column_name = $2"
	}

	var one int
	err := tx.QueryRowContext(ctx, probe, table, column).Scan(&one)
	switch {
	case err == nil:
		return nil
	case !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("probe column %s.%s: %w", table, column, err)
	}

	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("add column %s.%s: %w", table, column, err)
	}
	return nil
}

// scrubFreedPages erases the pages the rebuild released.
//
// On SQLite, DROP TABLE only moves pages to the freelist: the plaintext stays
// readable in the file until VACUUM rewrites it. In WAL mode VACUUM writes the
// clean pages to the -wal sidecar, so the secrets survive there until a
// truncating checkpoint folds them back and resets the log.
//
// Postgres needs neither step: the rebuild already wrote a new relfilenode and
// unlinked the file that held the plaintext.
//
// Both statements are idempotent, as a NoTx step must be.
func scrubFreedPages(dialect migrations.Dialect) func(context.Context, *sql.DB) error {
	return func(ctx context.Context, db *sql.DB) error {
		if dialect != migrations.SQLite {
			return nil
		}
		if _, err := db.ExecContext(ctx, "VACUUM"); err != nil {
			return fmt.Errorf("vacuum api_keys database: %w", err)
		}
		if _, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			return fmt.Errorf("checkpoint api_keys write-ahead log: %w", err)
		}
		return nil
	}
}
