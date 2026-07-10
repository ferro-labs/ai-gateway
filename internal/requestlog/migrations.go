package requestlog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/ferro-labs/ai-gateway/internal/migrations"
	"github.com/ferro-labs/ai-gateway/internal/sqldb"
)

// requestLogLedger is the request log store's own migration ledger, kept
// distinct from the key and config stores' so all three can share one physical
// database without their independent version sequences colliding.
const requestLogLedger = "request_log_schema_migrations"

// createdAtIndex serves List's created_at ordering and range, Delete's
// created_at range, and Stats' since filter.
const createdAtIndex = "idx_request_logs_created_at"

// requestLogSteps returns the migration sequence for the request_logs database.
//
// Version 1 is the pre-runner schema. Databases created before the runner
// existed already have this shape, so Run adopts it as a baseline rather than
// executing it. Version 2 builds the created_at index; it runs outside a
// transaction because the Postgres path uses CREATE INDEX CONCURRENTLY.
func requestLogSteps(dialect sqldb.Dialect) []migrations.Step {
	return []migrations.Step{
		{Version: 1, Name: "request_logs_baseline", SQL: requestLogBaselineDDL(dialect)},
		{Version: 2, Name: "request_logs_created_at_index", NoTx: func(ctx context.Context, db *sql.DB) error {
			return ensureCreatedAtIndex(ctx, db, dialect)
		}},
	}
}

func requestLogBaselineDDL(dialect sqldb.Dialect) string {
	if dialect == sqldb.Postgres {
		return `
CREATE TABLE IF NOT EXISTS request_logs (
	id BIGSERIAL PRIMARY KEY,
	trace_id TEXT,
	stage TEXT NOT NULL,
	model TEXT,
	provider TEXT,
	prompt_tokens INTEGER NOT NULL,
	completion_tokens INTEGER NOT NULL,
	total_tokens INTEGER NOT NULL,
	error_message TEXT,
	created_at TIMESTAMPTZ NOT NULL
);`
	}
	return `
CREATE TABLE IF NOT EXISTS request_logs (
	id INTEGER PRIMARY KEY,
	trace_id TEXT,
	stage TEXT NOT NULL,
	model TEXT,
	provider TEXT,
	prompt_tokens INTEGER NOT NULL,
	completion_tokens INTEGER NOT NULL,
	total_tokens INTEGER NOT NULL,
	error_message TEXT,
	created_at TIMESTAMP NOT NULL
);`
}

// ensureCreatedAtIndex builds the created_at index if it is missing.
//
// request_logs takes a write per request per stage. On Postgres a plain
// CREATE INDEX holds a lock that blocks those writes for the length of the
// build, which on an existing table means every other instance stalls while one
// restarts. Building concurrently trades a slower build for not blocking
// writers. It cannot run inside a transaction, which is why this is a NoTx step.
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
			slog.Warn("request log index build failed; queries will scan until the next start retries it",
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
		slog.Warn("request log index is invalid from an interrupted build; run REINDEX INDEX CONCURRENTLY to rebuild it",
			"index", createdAtIndex)
		return fmt.Errorf("request log index is invalid: %w", migrations.ErrDeferStep)
	default: // indexAbsent
		if _, err := db.ExecContext(ctx, "CREATE INDEX CONCURRENTLY IF NOT EXISTS "+createdAtIndex+" ON request_logs (created_at)"); err != nil {
			// A failed concurrent build usually leaves an invalid index behind,
			// which the next start reports as indexInvalid (and points at
			// REINDEX) rather than silently rebuilding. Until then queries scan.
			slog.Warn("request log index build failed; queries will scan until it is rebuilt with REINDEX INDEX CONCURRENTLY",
				"index", createdAtIndex, "error", err)
			return fmt.Errorf("request log index build failed: %w", migrations.ErrDeferStep)
		}
		return nil
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
		slog.Warn("could not probe request log index; assuming it is absent", "index", createdAtIndex, "error", err)
		return indexAbsent
	case valid:
		return indexValid
	default:
		return indexInvalid
	}
}
