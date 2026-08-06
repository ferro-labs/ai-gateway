package repository

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/migrations"
)

// schemaDialects are the dialects every DDL step must be expressible in.
var schemaDialects = []struct {
	name    string
	dialect migrations.Dialect
}{
	{name: "sqlite", dialect: migrations.SQLite},
	{name: "postgres", dialect: migrations.Postgres},
}

// schemaFiles lists the .sql basenames embedded for one dialect, without the
// suffix, so the result is the set of NNNN_name step identities that dialect
// provides.
func schemaFiles(t *testing.T, dialect migrations.Dialect) map[string]struct{} {
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
// expose the same step identities and, for every identity present in both,
// the same columns — same names, same order, same nullability.
//
// The filename comparison keys on the whole NNNN_name basename rather than the
// version alone, because two independent ledgers share this directory —
// api_keys and gateway_config both start at version 1 — so a bare version
// number does not identify a step. The basename does, and comparing it catches
// strictly more: an index added to one dialect and forgotten in the other, and
// a file renamed in one dialect only.
//
// The column comparison deliberately does not compare type spelling: postgres
// and sqlite diverge there for legitimate reasons (TIMESTAMPTZ/TIMESTAMP vs
// DATETIME, SMALLINT vs INTEGER), and a literal match would fail every file in
// this directory. Names, order, and nullability are exactly what a forgotten
// column edit gets wrong, so that is what parseCreateTableColumns extracts.
func TestSchemaDialectsMatch(t *testing.T) {
	sqliteFiles := schemaFiles(t, migrations.SQLite)
	postgresFiles := schemaFiles(t, migrations.Postgres)

	for name := range sqliteFiles {
		if _, ok := postgresFiles[name]; !ok {
			t.Errorf("schema/sqlite/%s.sql has no postgres counterpart", name)
		}
	}
	for name := range postgresFiles {
		if _, ok := sqliteFiles[name]; !ok {
			t.Errorf("schema/postgres/%s.sql has no sqlite counterpart", name)
		}
	}

	for name := range sqliteFiles {
		if _, ok := postgresFiles[name]; !ok {
			continue // already reported above; nothing to compare it against
		}
		sqliteCols := parseSchemaColumns(t, mustSchema(migrations.SQLite, name))
		postgresCols := parseSchemaColumns(t, mustSchema(migrations.Postgres, name))
		if !reflect.DeepEqual(sqliteCols, postgresCols) {
			t.Errorf("schema/%s.sql columns differ between dialects:\n  sqlite:   %+v\n  postgres: %+v", name, sqliteCols, postgresCols)
		}
	}
}

// parseSchemaColumns extracts the columns a DDL file defines, whichever shape
// it takes: the full list for a CREATE TABLE, or the added columns for a file
// of ALTER TABLE ... ADD COLUMN statements.
//
// A migration that adds a column is as prone to dialect drift as one that
// creates a table — more so, since the two files are written minutes apart and
// look almost identical — so it gets the same guard rather than an exemption.
func parseSchemaColumns(t *testing.T, ddl string) []schemaColumn {
	t.Helper()
	if strings.Contains(strings.ToUpper(stripSQLComments(ddl)), "ADD COLUMN") {
		return parseAddedColumns(t, ddl)
	}
	return parseCreateTableColumns(t, ddl)
}

// parseAddedColumns pulls the column added by each ALTER TABLE ... ADD COLUMN
// statement. Postgres spells the guard "IF NOT EXISTS" and SQLite has no
// equivalent, so that clause is normalised away before the column is read —
// otherwise every such pair would appear to differ on the name alone.
func parseAddedColumns(t *testing.T, ddl string) []schemaColumn {
	t.Helper()

	var columns []schemaColumn
	for _, statement := range strings.Split(stripSQLComments(ddl), ";") {
		upper := strings.ToUpper(statement)
		at := strings.Index(upper, "ADD COLUMN")
		if at < 0 {
			continue
		}
		def := strings.TrimSpace(statement[at+len("ADD COLUMN"):])
		if rest, found := strings.CutPrefix(strings.ToUpper(def), "IF NOT EXISTS"); found {
			def = strings.TrimSpace(def[len(def)-len(rest):])
		}
		fields := strings.Fields(def)
		if len(fields) == 0 {
			t.Fatalf("no column name after ADD COLUMN in: %s", statement)
		}
		tail := strings.ToUpper(strings.Join(fields[1:], " "))
		columns = append(columns, schemaColumn{
			name:     fields[0],
			nullable: strings.Contains(tail, "NULL") && !strings.Contains(tail, "NOT NULL"),
		})
	}
	if len(columns) == 0 {
		t.Fatalf("no added columns parsed from DDL: %s", ddl)
	}
	return columns
}

// stripSQLComments removes -- line comments so prose in a migration cannot be
// mistaken for SQL. The CREATE TABLE parser finds its body by locating the
// first "(", which a comment could otherwise supply.
func stripSQLComments(ddl string) string {
	var b strings.Builder
	for line := range strings.SplitSeq(ddl, "\n") {
		code, _, _ := strings.Cut(line, "--")
		b.WriteString(code)
		b.WriteString("\n")
	}
	return b.String()
}

// schemaColumn is one column's name and nullability, as pulled out of a
// CREATE TABLE body. Type is deliberately not captured — see
// TestSchemaDialectsMatch.
type schemaColumn struct {
	name     string
	nullable bool
}

// parseCreateTableColumns extracts the ordered column list from the first (and
// in every file here, only) CREATE TABLE statement in ddl.
//
// It is a small, deliberately dumb parser, not a SQL grammar: it finds the
// column-list parentheses by depth, splits on top-level commas, skips any
// segment that looks like a table-level constraint (PRIMARY KEY/FOREIGN
// KEY/UNIQUE/CHECK/CONSTRAINT), and for everything else takes the first token
// as the column name. Nullability comes from searching the definition after
// that first token for "NOT NULL" / "NULL" — a column with neither (e.g. a
// bare "id TEXT PRIMARY KEY") is treated as NOT NULL, matching how every
// PRIMARY KEY column in this schema is written on both dialects.
func parseCreateTableColumns(t *testing.T, ddl string) []schemaColumn {
	t.Helper()

	open := strings.Index(ddl, "(")
	if open < 0 {
		t.Fatalf("no column list in DDL: %s", ddl)
	}
	depth := 0
	closeAt := -1
	for i := open; i < len(ddl); i++ {
		switch ddl[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				closeAt = i
			}
		}
		if closeAt >= 0 {
			break
		}
	}
	if closeAt < 0 {
		t.Fatalf("unbalanced parens in DDL: %s", ddl)
	}

	var columns []schemaColumn
	for _, segment := range splitTopLevelCommas(ddl[open+1 : closeAt]) {
		def := strings.TrimSpace(segment)
		if def == "" || isTableConstraint(def) {
			continue
		}
		fields := strings.Fields(def)
		if len(fields) == 0 {
			continue
		}
		rest := strings.ToUpper(strings.Join(fields[1:], " "))
		nullable := strings.Contains(rest, "NULL") && !strings.Contains(rest, "NOT NULL")
		columns = append(columns, schemaColumn{name: fields[0], nullable: nullable})
	}
	if len(columns) == 0 {
		t.Fatalf("no columns parsed from DDL: %s", ddl)
	}
	return columns
}

// isTableConstraint reports whether def is a table-level constraint rather
// than a column definition. None of the schemas here have one today, but a
// column definition never legitimately starts with these keywords, so this
// stays safe to check unconditionally.
func isTableConstraint(def string) bool {
	upper := strings.ToUpper(def)
	for _, kw := range []string{"PRIMARY KEY", "FOREIGN KEY", "UNIQUE", "CHECK", "CONSTRAINT"} {
		if strings.HasPrefix(upper, kw) {
			return true
		}
	}
	return false
}

// splitTopLevelCommas splits body on commas that are not nested inside
// parentheses, so a column definition containing its own "(...)" (e.g. a
// DEFAULT expression) is not split in the middle.
func splitTopLevelCommas(body string) []string {
	var parts []string
	depth := 0
	start := 0
	for i, r := range body {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, body[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, body[start:])
	return parts
}

// TestSchemaFilesMatchSteps ties the embedded files to the Go manifests in both
// directions.
//
// Forward, it proves every SQL step resolves to a non-empty file whose name is
// the step's own version and name, so the literal passed to mustSchema cannot
// drift from the step it lands on and mustSchema cannot panic at startup.
// Backward, it proves no file is orphaned — a .sql no step references would
// otherwise look applied while never executing.
func TestSchemaFilesMatchSteps(t *testing.T) {
	for _, d := range schemaDialects {
		t.Run(d.name, func(t *testing.T) {
			steps := append(keyStoreSteps(d.dialect), configStoreSteps(d.dialect)...)
			steps = append(steps, sessionStoreSteps(d.dialect)...)
			steps = append(steps, auditStoreSteps(d.dialect)...)

			files := schemaFiles(t, d.dialect)

			referenced := make(map[string]struct{})
			for _, step := range steps {
				name := fmt.Sprintf("%04d_%s", step.Version, step.Name)
				if step.SQL == "" {
					// A Go step. It may still source its DDL from a file and run
					// it conditionally — the only way to make an ADD COLUMN
					// idempotent on SQLite, which has no IF NOT EXISTS — so a
					// file named for it is referenced, not orphaned. Its
					// contents cannot be compared against a step that carries no
					// SQL, only proved to resolve.
					if _, ok := files[name]; ok {
						referenced[name] = struct{}{}
						if strings.TrimSpace(mustSchema(d.dialect, name)) == "" {
							t.Errorf("schema/%s/%s.sql is empty", d.name, name)
						}
					}
					continue
				}
				referenced[name] = struct{}{}

				ddl := mustSchema(d.dialect, name)
				if strings.TrimSpace(ddl) == "" {
					t.Errorf("schema/%s/%s.sql is empty", d.name, name)
				}
				if ddl != step.SQL {
					t.Errorf("step %d (%s) SQL does not come from %s.sql", step.Version, step.Name, name)
				}
			}

			for name := range files {
				if _, ok := referenced[name]; !ok {
					t.Errorf("schema/%s/%s.sql is not referenced by any step", d.name, name)
				}
			}
		})
	}
}

// TestSchemaStepKinds pins which steps are DDL and which are Go, so a later
// change cannot quietly convert the key-hashing transform or the VACUUM scrub
// into a statement — neither is expressible as one.
func TestSchemaStepKinds(t *testing.T) {
	for _, d := range schemaDialects {
		t.Run(d.name, func(t *testing.T) {
			tests := []struct {
				version int
				name    string
				kind    string
			}{
				{version: 1, name: "api_keys_baseline", kind: "SQL"},
				{version: 2, name: "api_keys_hash", kind: "Fn"},
				{version: 3, name: "api_keys_scrub", kind: "NoTx"},
			}

			steps := keyStoreSteps(d.dialect)
			if len(steps) != len(tests) {
				t.Fatalf("keyStoreSteps has %d steps, want %d", len(steps), len(tests))
			}
			for i, tt := range tests {
				step := steps[i]
				if step.Version != tt.version || step.Name != tt.name {
					t.Errorf("step %d is %d (%s), want %d (%s)", i, step.Version, step.Name, tt.version, tt.name)
				}
				var got string
				switch {
				case step.SQL != "":
					got = "SQL"
				case step.Fn != nil:
					got = "Fn"
				case step.NoTx != nil:
					got = "NoTx"
				}
				if got != tt.kind {
					t.Errorf("step %d (%s) is a %s step, want %s", step.Version, step.Name, got, tt.kind)
				}
			}
		})
	}
}
