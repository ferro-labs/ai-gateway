// Package migrations provides a minimal versioned schema-migration runner for
// SQLite and Postgres. It is mechanism only: callers supply the SQL or Go steps
// (which they may embed with go:embed); the runner records which versions have
// been applied in a schema_migrations ledger and applies the rest in order.
package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/sqldb"
)

// Dialect selects the SQL flavor used for the ledger table and placeholder
// binding. It aliases sqldb.Dialect so the runner and every store share one
// dialect type rather than converting between parallel definitions.
type Dialect = sqldb.Dialect

// Supported dialects.
const (
	SQLite   = sqldb.SQLite
	Postgres = sqldb.Postgres
)

// Step is one migration for one database. Exactly one of SQL, Fn, or NoTx must
// be set. Steps are keyed by Version, which must be monotonic, unique, and
// ascending across the slice passed to Run.
type Step struct {
	// Version is the ledger key. Must be >= 1 and strictly greater than the
	// previous step's version.
	Version int
	// Name is recorded in schema_migrations alongside the version.
	Name string
	// SQL is the DDL for this dialect, executed inside the step transaction.
	SQL string
	// Fn is a Go step executed inside the step transaction.
	//
	// The SQLite pool runs with MaxOpenConns=1, so an Fn that holds an open
	// *sql.Rows cursor while issuing a write on the same *sql.Tx deadlocks
	// against itself. Read every row and close the cursor before writing.
	Fn func(ctx context.Context, tx *sql.Tx) error
	// NoTx runs statements that cannot execute inside a transaction (for
	// example VACUUM). It runs against the *sql.DB with no surrounding
	// transaction, so the ledger row is inserted separately after it returns.
	//
	// NoTx steps MUST be idempotent: a crash between the function returning and
	// the ledger insert committing re-runs them on the next call.
	NoTx func(ctx context.Context, db *sql.DB) error
}

// lockID identifies the advisory lock Postgres callers take for the duration of
// Run. The value is arbitrary; it only has to be stable and unlikely to collide
// with an application lock.
const lockID int64 = 4872193001

// defaultLedger is the ledger table Run uses. Stores that may share one physical
// database pass a distinct ledger via RunNamed so their version sequences do not
// collide.
const defaultLedger = "schema_migrations"

// Run applies steps against the default schema_migrations ledger. See RunNamed.
func Run(ctx context.Context, db *sql.DB, dialect Dialect, primaryTable string, steps []Step) error {
	return RunNamed(ctx, db, dialect, defaultLedger, primaryTable, steps)
}

// RunNamed applies every step whose version is not yet recorded, in ascending
// order, and is safe to call on every process start.
//
// ledger names the bookkeeping table. Each schema that might share a physical
// database with another (several Postgres tables in one instance) passes its own
// ledger name so their independent version sequences never collide in a shared
// ledger.
//
// If the ledger is empty, primaryTable is non-empty, and that table already
// exists, steps[0] is recorded as applied without executing it: the database
// predates the runner and its baseline schema is already present.
//
// Concurrent callers are serialized. On Postgres, where several gateway
// instances can share one database, RunNamed holds an advisory lock for its
// whole duration, so a second instance waits and then finds every step already
// recorded. SQLite serializes writers itself.
func RunNamed(ctx context.Context, db *sql.DB, dialect Dialect, ledger, primaryTable string, steps []Step) error {
	// Everything below treats "not Postgres" as SQLite — the ledger DDL, the
	// table probe, and the placeholder form. An unrecognized dialect would
	// silently take the SQLite path rather than fail.
	if dialect != SQLite && dialect != Postgres {
		return fmt.Errorf("migrations: unsupported dialect %q", dialect)
	}
	if ledger == "" {
		return errors.New("migrations: ledger table name is required")
	}
	if err := validate(steps); err != nil {
		return err
	}

	if dialect == Postgres {
		// The lock is held on its own reserved session; the steps below run on
		// other pooled connections. Postgres releases session-level advisory
		// locks when the backend disconnects, so a crash mid-migration cannot
		// strand it.
		conn, err := db.Conn(ctx)
		if err != nil {
			return fmt.Errorf("reserve migration connection: %w", err)
		}
		defer func() { _ = conn.Close() }()

		if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", lockID); err != nil {
			return fmt.Errorf("acquire migration lock: %w", err)
		}
		defer func() {
			// The caller's context may already be done; releasing the lock is
			// still worth attempting so the connection returns to the pool
			// unlocked rather than waiting on the backend to disconnect.
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", lockID)
		}()
	}

	if err := ensureLedger(ctx, db, dialect, ledger); err != nil {
		return err
	}
	applied, err := appliedVersions(ctx, db, ledger)
	if err != nil {
		return err
	}
	if err := checkNotAhead(applied, steps); err != nil {
		return err
	}

	// ledger is a caller-supplied constant, never user input, so it is safe to
	// interpolate into the statement text (identifiers cannot be bound).
	ledgerInsert := sqldb.Bind(dialect, "INSERT INTO "+ledger+"(version, name, applied_at) VALUES(?, ?, ?)")

	if len(applied) == 0 && primaryTable != "" {
		exists, err := tableExists(ctx, db, dialect, primaryTable)
		if err != nil {
			return err
		}
		if exists {
			base := steps[0]
			if _, err := db.ExecContext(ctx, ledgerInsert, base.Version, base.Name, time.Now().UTC()); err != nil {
				return fmt.Errorf("adopt baseline migration %d (%s): %w", base.Version, base.Name, err)
			}
			applied[base.Version] = struct{}{}
		}
	}

	for _, step := range steps {
		if _, done := applied[step.Version]; done {
			continue
		}
		if err := applyStep(ctx, db, ledgerInsert, step); err != nil {
			return err
		}
	}
	return nil
}

func applyStep(ctx context.Context, db *sql.DB, ledgerInsert string, step Step) error {
	now := time.Now().UTC()

	if step.NoTx != nil {
		if err := step.NoTx(ctx, db); err != nil {
			return fmt.Errorf("migration %d (%s): %w", step.Version, step.Name, err)
		}
		if _, err := db.ExecContext(ctx, ledgerInsert, step.Version, step.Name, now); err != nil {
			return fmt.Errorf("record migration %d (%s): %w", step.Version, step.Name, err)
		}
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %d (%s): %w", step.Version, step.Name, err)
	}

	if step.SQL != "" {
		if _, err := tx.ExecContext(ctx, step.SQL); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d (%s): %w", step.Version, step.Name, err)
		}
	} else if err := step.Fn(ctx, tx); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("migration %d (%s): %w", step.Version, step.Name, err)
	}

	if _, err := tx.ExecContext(ctx, ledgerInsert, step.Version, step.Name, now); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("record migration %d (%s): %w", step.Version, step.Name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d (%s): %w", step.Version, step.Name, err)
	}
	return nil
}

// checkNotAhead refuses to run against a database a newer binary has already
// migrated. Rolling back a deployment leaves the schema at the newer shape,
// which this binary's statements were not written against; stopping is safer
// than reading and writing rows through a schema it does not know.
func checkNotAhead(applied map[int]struct{}, steps []Step) error {
	known := make(map[int]struct{}, len(steps))
	for _, step := range steps {
		known[step.Version] = struct{}{}
	}
	for version := range applied {
		if _, ok := known[version]; !ok {
			return fmt.Errorf("migrations: database has migration %d applied, which this build does not know; it was migrated by a newer version", version)
		}
	}
	return nil
}

func validate(steps []Step) error {
	if len(steps) == 0 {
		return errors.New("migrations: at least one step is required")
	}
	prev := 0
	for i, step := range steps {
		if step.Version < 1 {
			return fmt.Errorf("migrations: step %d has version %d; versions must be >= 1", i, step.Version)
		}
		if i > 0 && step.Version <= prev {
			return fmt.Errorf("migrations: step versions must be strictly ascending; version %d follows %d", step.Version, prev)
		}
		prev = step.Version
		if step.Name == "" {
			return fmt.Errorf("migrations: step %d (version %d) has an empty name", i, step.Version)
		}
		set := 0
		if step.SQL != "" {
			set++
		}
		if step.Fn != nil {
			set++
		}
		if step.NoTx != nil {
			set++
		}
		if set != 1 {
			return fmt.Errorf("migrations: step %d (version %d) must set exactly one of SQL, Fn, or NoTx (found %d)", i, step.Version, set)
		}
	}
	return nil
}

func ensureLedger(ctx context.Context, db *sql.DB, dialect Dialect, ledger string) error {
	appliedAt := "DATETIME"
	if dialect == Postgres {
		appliedAt = "TIMESTAMPTZ"
	}
	// ledger and appliedAt are trusted internal constants, never user input;
	// table and column names cannot be bound as parameters.
	// #nosec G202
	ddl := "CREATE TABLE IF NOT EXISTS " + ledger +
		" (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at " + appliedAt + " NOT NULL)"
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("create %s ledger table: %w", ledger, err)
	}
	return nil
}

func appliedVersions(ctx context.Context, db *sql.DB, ledger string) (map[int]struct{}, error) {
	// ledger is a trusted internal constant, not user input.
	// #nosec G202
	rows, err := db.QueryContext(ctx, "SELECT version FROM "+ledger)
	if err != nil {
		return nil, fmt.Errorf("read applied migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	applied := make(map[int]struct{})
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scan applied migration: %w", err)
		}
		applied[v] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate applied migrations: %w", err)
	}
	return applied, nil
}

// tableExists reports whether table is present. primaryTable is caller-supplied
// and never user input, but is passed as a bound parameter rather than
// interpolated into the query text.
func tableExists(ctx context.Context, db *sql.DB, dialect Dialect, table string) (bool, error) {
	if dialect == Postgres {
		var exists bool
		if err := db.QueryRowContext(ctx, "SELECT to_regclass($1) IS NOT NULL", table).Scan(&exists); err != nil {
			return false, fmt.Errorf("probe table %q: %w", table, err)
		}
		return exists, nil
	}

	var one int
	err := db.QueryRowContext(ctx, "SELECT 1 FROM sqlite_master WHERE type='table' AND name = ?", table).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("probe table %q: %w", table, err)
	}
	return true, nil
}
