package requestlog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/ferro-labs/ai-gateway/internal/migrations"
	"github.com/ferro-labs/ai-gateway/internal/sqldb"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
)

// requestLogLedger is the request log store's own migration ledger, kept
// distinct from the key and config stores' so all three can share one physical
// database without their independent version sequences colliding.
const requestLogLedger = "request_log_schema_migrations"

// createdAtIndex serves List's created_at ordering and range, Delete's
// created_at range, and Stats' since filter.
const createdAtIndex = "idx_request_logs_created_at"

// timingAndCostColumns are the per-request measurements version 3 adds.
var timingAndCostColumns = []string{"duration_ms", "ttft_ms", "cost_usd"}

// doubleType is the type those three columns carry, spelled so both backends
// store the same value. Postgres REAL is a 4-byte float4 carrying about seven
// significant digits, which visibly rounds a per-request cost, while SQLite REAL
// is already an 8-byte double — so REAL alone makes the stored cost depend on
// the backend. DOUBLE PRECISION is float8 on Postgres and takes SQLite's REAL
// affinity, so one spelling gives both the same precision.
const doubleType = "DOUBLE PRECISION"

// requestLogSteps returns the migration sequence for the request_logs database.
//
// Version 1 is the pre-runner schema. Databases created before the runner
// existed already have this shape, so Run adopts it as a baseline rather than
// executing it. Version 2 builds the created_at index; it runs outside a
// transaction because the Postgres path uses CREATE INDEX CONCURRENTLY.
func requestLogSteps(dialect sqldb.Dialect) []migrations.Step {
	return []migrations.Step{
		{Version: 1, Name: "request_logs_baseline", SQL: mustSchema(dialect, "0001_request_logs_baseline")},
		{Version: 2, Name: "request_logs_created_at_index", NoTx: func(ctx context.Context, db *sql.DB) error {
			return ensureCreatedAtIndex(ctx, db, dialect)
		}},
		{Version: 3, Name: "request_logs_timing_and_cost", Fn: addTimingAndCostColumns(dialect)},
		{Version: 4, Name: "request_logs_widen_timing_and_cost", Fn: widenTimingAndCostColumns(dialect)},
		{Version: 5, Name: "request_logs_api_key", Fn: addAPIKeyColumn(dialect)},
	}
}

// addAPIKeyColumn adds the credential a request was served under.
//
// Nullable, and existing rows keep NULL rather than being backfilled: a row
// written before the column existed makes no claim about who consumed it, and
// the empty string would read as "no credential was presented", which is a
// different and checkable fact.
//
// Probed before it is added for the same reason the timing columns are: a
// request_logs restored without its ledger has its baseline adopted and every
// later step replayed, and a bare ALTER against a table that already has the
// column wedges every subsequent start.
func addAPIKeyColumn(dialect sqldb.Dialect) func(context.Context, *sql.Tx) error {
	return func(ctx context.Context, tx *sql.Tx) error {
		exists, err := columnExists(ctx, tx, dialect, "request_logs", "api_key_id")
		if err != nil {
			return err
		}
		if exists {
			return nil
		}
		if _, err := tx.ExecContext(ctx, "ALTER TABLE request_logs ADD COLUMN api_key_id TEXT"); err != nil {
			return fmt.Errorf("add request log column api_key_id: %w", err)
		}
		return nil
	}
}

// addTimingAndCostColumns adds the per-request measurements the gateway already
// computes but never stored: end-to-end duration, time to first token, and
// estimated cost.
//
// All three are nullable and existing rows keep NULL rather than being
// backfilled with zero. Zero is a measurement — "this request cost nothing",
// "the first token arrived instantly" — and rows written before this column
// existed carry no such claim. The distinction survives into the API and the
// dashboard, which report an unpriced request separately from a free one.
//
// A loop rather than one statement per column written out three times, and a Go
// step rather than a .sql file: SQLite has no ADD COLUMN IF NOT EXISTS, so the
// text would have to be repeated per dialect to gain nothing. All three run in
// the step transaction, so the column set moves as a unit.
//
// Each column is probed before it is added, which keeps the step re-runnable
// against a table that already has them. The step transaction covers both
// dialects' DDL, so no crash can leave the columns added and the version
// unrecorded — but a request_logs restored without its ledger can, and RunNamed
// then adopts the baseline and replays every later step against a table that is
// already migrated. A bare ALTER wedges every subsequent start there.
func addTimingAndCostColumns(dialect sqldb.Dialect) func(context.Context, *sql.Tx) error {
	return func(ctx context.Context, tx *sql.Tx) error {
		for _, column := range timingAndCostColumns {
			exists, err := columnExists(ctx, tx, dialect, "request_logs", column)
			if err != nil {
				return err
			}
			if exists {
				continue
			}
			// #nosec G202 -- column names are fixed literals from the loop above.
			if _, err := tx.ExecContext(ctx, "ALTER TABLE request_logs ADD COLUMN "+column+" "+doubleType); err != nil {
				return fmt.Errorf("add request log column %s: %w", column, err)
			}
		}
		return nil
	}
}

// widenTimingAndCostColumns converts the three columns to doubleType on a
// Postgres database that took them as float4 from an earlier version 3.
//
// SQLite needs nothing: REAL there is already an 8-byte double and the declared
// type only sets affinity, so the value it stores does not change. On Postgres
// the ALTER rewrites the table where the columns really are float4, and is a
// catalog no-op where version 3 already created them as float8. Costs already
// rounded to float4 stay rounded; widening stops later writes from losing
// digits, it does not restore digits already lost.
func widenTimingAndCostColumns(dialect sqldb.Dialect) func(context.Context, *sql.Tx) error {
	return func(ctx context.Context, tx *sql.Tx) error {
		for _, statement := range widenStatements(dialect) {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("widen request log columns: %w", err)
			}
		}
		return nil
	}
}

// widenStatements returns the DDL that gives the version 3 columns doubleType on
// dialect, and nothing at all for SQLite.
func widenStatements(dialect sqldb.Dialect) []string {
	if dialect != sqldb.Postgres {
		return nil
	}
	statements := make([]string, 0, len(timingAndCostColumns))
	for _, column := range timingAndCostColumns {
		// #nosec G202 -- column names are fixed literals from timingAndCostColumns.
		statements = append(statements, "ALTER TABLE request_logs ALTER COLUMN "+column+" TYPE "+doubleType)
	}
	return statements
}

// columnExists reports whether table already has column. It probes the catalog
// rather than executing the ALTER and matching the resulting error message,
// which differs by driver.
//
// The Postgres probe resolves the table through to_regclass so it inspects the
// relation the surrounding statements will actually touch; filtering
// information_schema.columns by name alone would match a same-named table in any
// visible schema. attisdropped excludes columns Postgres keeps in the catalog
// after a DROP COLUMN.
func columnExists(ctx context.Context, tx *sql.Tx, dialect sqldb.Dialect, table, column string) (bool, error) {
	probe := "SELECT 1 FROM pragma_table_info(?) WHERE name = ?"
	if dialect == sqldb.Postgres {
		probe = `SELECT 1 FROM pg_attribute
WHERE attrelid = to_regclass($1) AND attname = $2 AND attnum > 0 AND NOT attisdropped`
	}

	var one int
	switch err := tx.QueryRowContext(ctx, probe, table, column).Scan(&one); {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	default:
		return false, fmt.Errorf("probe column %s.%s: %w", table, column, err)
	}
}

// ensureCreatedAtIndex builds the created_at index if it is missing.
//
// request_logs takes a write per request per stage. On Postgres a plain
// CREATE INDEX holds a lock that blocks those writes for the length of the
// build, which on an existing table means every other instance stalls while one
// restarts. Building concurrently trades a slower build for not blocking
// writers. It cannot run inside a transaction, which is why this is a NoTx step.
//
// Which of the two runs is decided by whether the table has rows, not by which
// migration this is. On a fresh database this step follows the CREATE TABLE in
// the same run, so it indexes an empty table: there are no writers to avoid
// blocking and no rows to scan, and CREATE INDEX CONCURRENTLY buys nothing while
// costing a wait for every open snapshot in the database. The same step also
// runs against a populated table — a database that predates the runner has its
// baseline adopted and every later step replayed — and there the concurrent
// build is the whole point. Probing for a row distinguishes the two; a probe
// that fails is treated as populated, so an unreadable table takes the build
// that is safe under live traffic.
//
// A missing index costs query time, not correctness, so a failed or incomplete
// build is non-fatal: the step returns migrations.ErrDeferStep so the runner
// keeps startup alive but does not record the version, and the next start
// retries the build. Only a valid index records the step as done.
func ensureCreatedAtIndex(ctx context.Context, db *sql.DB, dialect sqldb.Dialect) error {
	if dialect != sqldb.Postgres {
		// createdAtIndex is a package constant, not input; identifiers cannot be
		// bound as parameters.
		if _, err := db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS "+createdAtIndex+" ON request_logs (created_at)"); err != nil {
			logger.Default().Warn("request log index build failed; queries will scan until the next start retries it",
				"index", createdAtIndex, "error", err)
			return fmt.Errorf("build request log index: %w", migrations.ErrDeferStep)
		}
		return nil
	}

	switch postgresIndexState(ctx, db) {
	case indexValid:
		return nil
	case indexInvalid:
		// An interrupted CREATE INDEX CONCURRENTLY leaves the index in place but
		// marked invalid, and CREATE ... IF NOT EXISTS then skips it forever.
		// Healing it means dropping and rebuilding, but a bare-name check cannot
		// tell an abandoned index from one another instance is building right
		// now, and dropping that would abort the live build. So this logs and
		// defers to the operator rather than coordinating a fleet-wide drop. A
		// self-healing rebuild would need an advisory lock around the whole
		// probe/drop/create; add it only if interrupted builds prove common.
		// Deferring keeps the step unrecorded, so once the operator rebuilds it
		// the next start records it.
		logger.Default().Warn("request log index is invalid from an interrupted build; run REINDEX INDEX CONCURRENTLY to rebuild it",
			"index", createdAtIndex)
		return fmt.Errorf("request log index is invalid: %w", migrations.ErrDeferStep)
	default: // indexAbsent
		if err := buildPostgresCreatedAtIndex(ctx, db); err != nil {
			// A failed concurrent build usually leaves an invalid index behind,
			// which the next start reports as indexInvalid (and points at
			// REINDEX) rather than silently rebuilding. Until then queries scan.
			logger.Default().Warn("request log index build failed; queries will scan until it is rebuilt with REINDEX INDEX CONCURRENTLY",
				"index", createdAtIndex, "error", err)
			return fmt.Errorf("request log index build failed: %w", migrations.ErrDeferStep)
		}
		return nil
	}
}

// buildPostgresCreatedAtIndex builds the index the way the table's contents
// warrant. See ensureCreatedAtIndex for why the two cases differ.
func buildPostgresCreatedAtIndex(ctx context.Context, db *sql.DB) error {
	// createdAtIndex is a package constant, not input; identifiers cannot be
	// bound as parameters.
	const create = "CREATE INDEX IF NOT EXISTS "
	const createConcurrently = "CREATE INDEX CONCURRENTLY IF NOT EXISTS "
	const target = " ON request_logs (created_at)"

	if !requestLogsHasRows(ctx, db) {
		// Empty table: the build is instantaneous, so a lock wait means someone
		// else holds the table and the build should fail rather than queue —
		// Postgres serves lock requests in order, so waiting here would stall
		// every query that arrives behind it.
		conn, err := db.Conn(ctx)
		if err != nil {
			return fmt.Errorf("reserve index build connection: %w", err)
		}
		defer func() { _ = conn.Close() }()

		if _, err := conn.ExecContext(ctx, "SET lock_timeout = '"+migrations.PostgresLockTimeout+"'"); err != nil {
			return fmt.Errorf("set lock_timeout for index build: %w", err)
		}
		// The connection returns to the pool, so the session setting must not.
		defer func() { _, _ = conn.ExecContext(context.WithoutCancel(ctx), "RESET lock_timeout") }()

		_, err = conn.ExecContext(ctx, create+createdAtIndex+target)
		return err
	}

	// Populated table: build concurrently so live writers are not blocked. Say
	// so — this is the one migration step that can take minutes, and an operator
	// watching a slow start should not have to guess why.
	logger.Default().Info("building the request log index concurrently on a populated table; startup continues when it completes",
		"index", createdAtIndex)

	// Deliberately no lock_timeout — a concurrent build waits for open snapshots
	// by design, and cancelling that wait would mean the index is never built on
	// any database with ordinary traffic. A build blocked behind an abandoned
	// session is an operator concern; idle_in_transaction_session_timeout is the
	// server-side control for it.
	_, err := db.ExecContext(ctx, createConcurrently+createdAtIndex+target)
	return err
}

// requestLogsHasRows reports whether request_logs holds at least one row. A
// probe failure reports true, so an unreadable table takes the concurrent build.
func requestLogsHasRows(ctx context.Context, db *sql.DB) bool {
	var one int
	switch err := db.QueryRowContext(ctx, "SELECT 1 FROM request_logs LIMIT 1").Scan(&one); {
	case errors.Is(err, sql.ErrNoRows):
		return false
	case err != nil:
		logger.Default().Warn("could not probe request log table; building the index concurrently",
			"index", createdAtIndex, "error", err)
		return true
	default:
		return true
	}
}

type indexState int

const (
	indexAbsent indexState = iota
	indexValid
	indexInvalid
)

// postgresIndexState reports whether the created_at index is absent, present
// and usable, or present but invalid.
//
// to_regclass resolves the name through search_path, so the probe inspects the
// index the writer would actually use rather than a same-named index in another
// schema. It returns NULL — and the join no rows — when the index does not
// exist. A probe failure is treated as absent so a transient error at most
// triggers a redundant IF NOT EXISTS build.
func postgresIndexState(ctx context.Context, db *sql.DB) indexState {
	const probe = `SELECT i.indisvalid FROM pg_index i WHERE i.indexrelid = to_regclass($1)`

	var valid bool
	switch err := db.QueryRowContext(ctx, probe, createdAtIndex).Scan(&valid); {
	case errors.Is(err, sql.ErrNoRows):
		return indexAbsent
	case err != nil:
		logger.Default().Warn("could not probe request log index; assuming it is absent", "index", createdAtIndex, "error", err)
		return indexAbsent
	case valid:
		return indexValid
	default:
		return indexInvalid
	}
}
