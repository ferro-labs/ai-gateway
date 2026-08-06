package repository

import (
	"embed"
	"fmt"

	"github.com/ferro-labs/ai-gateway/internal/migrations"
)

// schemaFS holds the DDL for the migration steps that are pure SQL, one file
// per step per dialect, named NNNN_name.sql after the step's version and name.
//
// Only steps whose whole body is DDL live here. A step that runs Go code (Fn)
// or runs outside a transaction (NoTx) has no file, because its behaviour is
// not expressible as a statement. The ordered manifest therefore stays in Go —
// see keyStoreSteps and configStoreSteps — and this directory is a store of
// statement text, not a source of ordering: nothing here is discovered or
// sequenced by filename.
//
// Both dialects must carry the same set of files. TestSchemaDialectsMatch
// enforces that, so an index added to one dialect and forgotten in the other
// fails the build rather than shipping a schema that silently differs per
// backend.
//
//go:embed schema/sqlite/*.sql schema/postgres/*.sql
var schemaFS embed.FS

// schemaDir maps a dialect to its directory under schema/. It mirrors the
// migration runner in treating any non-Postgres dialect as SQLite; RunNamed
// rejects an unrecognized dialect before any step is read.
func schemaDir(dialect migrations.Dialect) string {
	if dialect == migrations.Postgres {
		return "postgres"
	}
	return "sqlite"
}

// mustSchema returns the DDL for one step, named as NNNN_name without the .sql
// suffix.
//
// The files are embedded at build time, so a name that does not resolve is a
// programming error rather than a runtime condition, and there is no schema to
// fall back to. TestSchemaFilesMatchSteps resolves every name both manifests
// use, so an unresolvable one fails the test suite before it can reach a
// running gateway.
func mustSchema(dialect migrations.Dialect, name string) string {
	path := "schema/" + schemaDir(dialect) + "/" + name + ".sql"
	ddl, err := schemaFS.ReadFile(path)
	if err != nil {
		panic(fmt.Sprintf("admin: embedded schema %s: %v", path, err))
	}
	return string(ddl)
}
