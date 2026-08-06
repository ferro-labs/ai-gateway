package requestlog

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/sqldb"
)

// schemaDialects are the dialects every DDL step must be expressible in.
var schemaDialects = []struct {
	name    string
	dialect sqldb.Dialect
}{
	{name: "sqlite", dialect: sqldb.SQLite},
	{name: "postgres", dialect: sqldb.Postgres},
}

// postgresOnlySchemaFiles names the .sql files that exist for Postgres alone,
// each with the reason it cannot be written for SQLite — a partitioned table or
// a concurrent-safe constraint has no SQLite spelling.
//
// It is empty today, and that is the point of declaring it: a deliberate
// divergence is written down here where a reviewer sees it and has to justify
// it, while a file simply forgotten in schema/sqlite/ is still an unexplained
// mismatch and still fails TestSchemaDialectsMatch. Adding a name here is a
// decision; leaving it out is not an escape hatch.
func postgresOnlySchemaFiles() map[string]string {
	return map[string]string{}
}

// schemaFiles lists the .sql basenames embedded for one dialect, without the
// suffix, so the result is the set of NNNN_name step identities that dialect
// provides.
func schemaFiles(t *testing.T, dialect sqldb.Dialect) map[string]struct{} {
	t.Helper()

	dir := "schema/" + schemaDir(dialect)
	entries, err := schemaFS.ReadDir(dir)
	if err != nil {
		t.Fatalf("read embedded %s: %v", dir, err)
	}

	files := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		files[strings.TrimSuffix(e.Name(), ".sql")] = struct{}{}
	}
	if len(files) == 0 {
		t.Fatalf("no .sql files embedded for %s", dir)
	}
	return files
}

// TestSchemaDialectsMatch is the drift guard: both dialect directories must
// expose the same step identities, except those declared in
// postgresOnlySchemaFiles, and for every identity present in both, the same
// columns in the same order.
//
// Without it, a column added to one baseline and forgotten in the other ships a
// request_logs table whose shape depends on the backend. Nothing fails at
// startup — the migration applies cleanly on both — and the mismatch surfaces
// only when a query scans a column one deployment does not have.
//
// The column comparison deliberately ignores type spelling: the two baselines
// legitimately differ there (BIGSERIAL vs INTEGER, TIMESTAMPTZ vs TIMESTAMP),
// so a literal match would fail on a correct schema. Names and order are what a
// forgotten column edit gets wrong, so that is what is compared.
func TestSchemaDialectsMatch(t *testing.T) {
	sqliteFiles := schemaFiles(t, sqldb.SQLite)
	postgresFiles := schemaFiles(t, sqldb.Postgres)
	postgresOnly := postgresOnlySchemaFiles()

	for name := range sqliteFiles {
		if _, ok := postgresFiles[name]; !ok {
			t.Errorf("schema/sqlite/%s.sql has no postgres counterpart", name)
		}
	}
	for name := range postgresFiles {
		if _, ok := sqliteFiles[name]; ok {
			continue
		}
		if reason, declared := postgresOnly[name]; !declared {
			t.Errorf("schema/postgres/%s.sql has no sqlite counterpart; write one, or declare it in postgresOnlySchemaFiles() with the reason it cannot exist", name)
		} else if strings.TrimSpace(reason) == "" {
			t.Errorf("postgresOnlySchemaFiles()[%q] has no reason", name)
		}
	}

	// A name left in the allowlist after its sqlite counterpart arrives — or
	// after the postgres file is deleted — would silently exempt a future file
	// of the same name from the parity check.
	for name := range postgresOnly {
		if _, ok := postgresFiles[name]; !ok {
			t.Errorf("postgresOnlySchemaFiles() names %q, which no postgres file provides", name)
		}
		if _, ok := sqliteFiles[name]; ok {
			t.Errorf("postgresOnlySchemaFiles() names %q, but schema/sqlite/%s.sql exists; remove the allowlist entry", name, name)
		}
	}

	for name := range sqliteFiles {
		if _, ok := postgresFiles[name]; !ok {
			continue // already reported above; nothing to compare it against
		}
		sqliteCols := schemaColumnNames(t, mustSchema(sqldb.SQLite, name))
		postgresCols := schemaColumnNames(t, mustSchema(sqldb.Postgres, name))
		if !reflect.DeepEqual(sqliteCols, postgresCols) {
			t.Errorf("schema/%s.sql columns differ between dialects:\n  sqlite:   %v\n  postgres: %v", name, sqliteCols, postgresCols)
		}
	}
}

// schemaColumnNames extracts the ordered column names from a CREATE TABLE file.
//
// It is not a SQL parser: every file in this directory is one CREATE TABLE with
// one column per line, so taking the first token of each line inside the
// parentheses is enough, and a file that stops matching that shape fails the
// t.Fatalf below rather than being silently mis-parsed.
func schemaColumnNames(t *testing.T, ddl string) []string {
	t.Helper()

	open := strings.Index(ddl, "(")
	closeAt := strings.LastIndex(ddl, ")")
	if open < 0 || closeAt < open {
		t.Fatalf("no column list in DDL: %s", ddl)
	}

	var names []string
	for line := range strings.SplitSeq(ddl[open+1:closeAt], "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 {
			continue
		}
		names = append(names, strings.TrimSuffix(fields[0], ","))
	}
	if len(names) == 0 {
		t.Fatalf("no columns parsed from DDL: %s", ddl)
	}
	return names
}

// TestSchemaFilesMatchSteps ties the embedded files to requestLogSteps in both
// directions.
//
// Forward, it proves every SQL step resolves to a non-empty file named for that
// step's own version and name, so the literal passed to mustSchema cannot drift
// from the step it lands on and mustSchema cannot panic when a writer opens.
// Backward, it proves no file is orphaned: a .sql no step references never
// executes, so the table it describes would not exist however correct the file
// looks.
func TestSchemaFilesMatchSteps(t *testing.T) {
	for _, d := range schemaDialects {
		t.Run(d.name, func(t *testing.T) {
			referenced := make(map[string]struct{})
			for _, step := range requestLogSteps(d.dialect) {
				if step.SQL == "" {
					// A NoTx or Fn step; its behaviour is Go, not DDL. See schema.go.
					continue
				}
				name := fmt.Sprintf("%04d_%s", step.Version, step.Name)
				referenced[name] = struct{}{}

				ddl := mustSchema(d.dialect, name)
				if strings.TrimSpace(ddl) == "" {
					t.Errorf("schema/%s/%s.sql is empty", d.name, name)
				}
				if ddl != step.SQL {
					t.Errorf("step %d (%s) SQL does not come from %s.sql", step.Version, step.Name, name)
				}
			}

			for name := range schemaFiles(t, d.dialect) {
				if _, ok := referenced[name]; !ok {
					t.Errorf("schema/%s/%s.sql is not referenced by any step", d.name, name)
				}
			}
		})
	}
}
