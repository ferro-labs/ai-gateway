package requestlog

import (
	"embed"
	"fmt"

	"github.com/ferro-labs/ai-gateway/internal/sqldb"
)

// schemaFS holds the DDL for the migration steps that are pure SQL, one file
// per step per dialect, named NNNN_name.sql after the step's version and name.
//
// Only version 1 qualifies, which is why 0002 through 0004 are absent rather
// than missing: version 2 builds the created_at index outside a transaction and
// decides between CREATE INDEX CONCURRENTLY, a plain build, and deferring on an
// invalid index; version 3 adds three columns one statement at a time, each only
// if the table does not already have it; and version 4 widens those columns on
// Postgres and does nothing at all on SQLite. None is a statement, so none can
// live in a file; all keep their Go bodies in migrations.go.
//
// Ordering therefore stays in Go — see requestLogSteps. Nothing here is
// discovered or sequenced by filename; this directory is a store of statement
// text keyed by a name the manifest already knows.
//
// Both dialects must carry the same set of files unless the divergence is
// declared. TestSchemaDialectsMatch enforces that, so a column added to the
// Postgres baseline and forgotten in the SQLite one fails the test suite rather
// than shipping a request_logs table whose shape depends on the backend — a
// difference that would otherwise surface as a scan error on one deployment
// and nowhere else.
//
//go:embed schema/sqlite/*.sql schema/postgres/*.sql
var schemaFS embed.FS

// schemaDir maps a dialect to its directory under schema/. Like the migration
// runner, it treats any non-Postgres dialect as SQLite; RunNamed rejects an
// unrecognized dialect before any step is read.
func schemaDir(dialect sqldb.Dialect) string {
	if dialect == sqldb.Postgres {
		return "postgres"
	}
	return "sqlite"
}

// mustSchema returns the DDL for one step, named as NNNN_name without the .sql
// suffix.
//
// The files are embedded at build time, so a name that does not resolve is a
// typo in this package, not a runtime condition, and there is no schema to fall
// back to: a writer that continues without its baseline table fails on every
// insert instead. TestSchemaFilesMatchSteps resolves every name the manifest
// uses, so an unresolvable one fails the test suite before it can panic in a
// running gateway.
func mustSchema(dialect sqldb.Dialect, name string) string {
	path := "schema/" + schemaDir(dialect) + "/" + name + ".sql"
	ddl, err := schemaFS.ReadFile(path)
	if err != nil {
		panic(fmt.Sprintf("requestlog: embedded schema %s: %v", path, err))
	}
	return string(ddl)
}
