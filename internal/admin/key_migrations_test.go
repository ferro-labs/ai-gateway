package admin

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// plaintextKey is a recognizable marker so the on-disk assertions below can
// prove the secret is gone rather than merely absent from the table.
const plaintextKey = "fgw_deadbeefcafe0123456789abcdef0123456789abcdef0123456789abcdef01"

// legacySchema is the api_keys table as v1.1.20 and earlier created it: the
// secret in a plaintext UNIQUE column, with a redundant secondary index.
const legacySchema = `
CREATE TABLE api_keys (
	id TEXT PRIMARY KEY,
	key TEXT UNIQUE NOT NULL,
	name TEXT NOT NULL,
	scopes TEXT NOT NULL,
	created_at DATETIME NOT NULL,
	revoked_at DATETIME NULL,
	expires_at DATETIME NULL,
	rotated_at DATETIME NULL,
	active BOOLEAN NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_api_keys_key ON api_keys(key);`

// usageColumns are the two columns earlier releases bolted on with a bare
// ALTER TABLE. A database last opened by a release that predates them is
// adopted at the baseline without them.
const usageColumns = `
ALTER TABLE api_keys ADD COLUMN usage_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE api_keys ADD COLUMN last_used_at DATETIME NULL;`

// writeLegacyDB builds a pre-migration database holding one plaintext key.
func writeLegacyDB(t *testing.T, withUsageColumns bool) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "keys.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	defer func() { _ = db.Close() }()

	ddl := legacySchema
	if withUsageColumns {
		ddl += usageColumns
	}
	if _, err := db.ExecContext(t.Context(), ddl); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}

	insert := `INSERT INTO api_keys(id, key, name, scopes, created_at, revoked_at, expires_at, rotated_at, active)
VALUES('legacy-id', ?, 'legacy', '["admin"]', ?, NULL, NULL, NULL, 1)`
	if _, err := db.ExecContext(t.Context(), insert, plaintextKey, time.Now().UTC()); err != nil {
		t.Fatalf("insert legacy key: %v", err)
	}
	return path
}

// dbArtifacts returns the database file plus any sidecars SQLite may have left
// behind. A secret surviving in the write-ahead log is just as recoverable as
// one in the main file.
func dbArtifacts(t *testing.T, path string) map[string][]byte {
	t.Helper()

	artifacts := make(map[string][]byte)
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		raw, err := os.ReadFile(path + suffix) //nolint:gosec // G304: path is a test temp file.
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("read %s%s: %v", path, suffix, err)
		}
		artifacts[path+suffix] = raw
	}
	return artifacts
}

func assertNoPlaintextOnDisk(t *testing.T, path string) {
	t.Helper()

	for name, raw := range dbArtifacts(t, path) {
		if n := bytes.Count(raw, []byte(plaintextKey)); n > 0 {
			t.Errorf("%s still holds the plaintext key %d time(s)", name, n)
		}
		// The prefix alone is enough to identify the secret's remains even if
		// a page boundary splits the full value.
		if n := bytes.Count(raw, []byte("fgw_deadbeefcafe")); n > 0 {
			t.Errorf("%s still holds a fragment of the plaintext key %d time(s)", name, n)
		}
	}
}

func columnNames(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()

	rows, err := db.QueryContext(t.Context(), "SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		t.Fatalf("read columns of %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan column name: %v", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate columns: %v", err)
	}
	return names
}

func hasColumn(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// A database written by an earlier release is migrated in place: the key it
// holds keeps working, but the secret itself is replaced by its hash.
func TestMigrationHashesExistingKeys(t *testing.T) {
	for _, tc := range []struct {
		name             string
		withUsageColumns bool
	}{
		{name: "current baseline", withUsageColumns: true},
		{name: "predates usage columns", withUsageColumns: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeLegacyDB(t, tc.withUsageColumns)

			store, err := NewSQLiteStore(t.Context(), path)
			if err != nil {
				t.Fatalf("open migrated store: %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })

			key, ok := store.ValidateKey(context.Background(), plaintextKey)
			if !ok {
				t.Fatal("the pre-migration key no longer authenticates")
			}
			if key.ID != "legacy-id" {
				t.Fatalf("validated key id = %q, want legacy-id", key.ID)
			}
			if key.Key != displayKey(plaintextKey) {
				t.Fatalf("stored display key = %q, want %q", key.Key, displayKey(plaintextKey))
			}

			var storedHash string
			if err := store.db.QueryRowContext(t.Context(), "SELECT key_hash FROM api_keys WHERE id = 'legacy-id'").Scan(&storedHash); err != nil {
				t.Fatalf("read key_hash: %v", err)
			}
			if storedHash != hashKey(plaintextKey) {
				t.Fatalf("key_hash = %q, want %q", storedHash, hashKey(plaintextKey))
			}

			cols := columnNames(t, store.db, "api_keys")
			if hasColumn(cols, "key") {
				t.Fatalf("the plaintext column survived the migration: %v", cols)
			}
			for _, want := range []string{"key_hash", "key_display", "usage_count", "last_used_at"} {
				if !hasColumn(cols, want) {
					t.Fatalf("column %s is missing after migration: %v", want, cols)
				}
			}
		})
	}
}

// Dropping the column is not deleting the secret. SQLite moves the freed pages
// to its freelist, where the plaintext stays readable until VACUUM rewrites the
// file. This is the assertion that makes the release's claim true; a table-level
// check passes either way.
func TestMigrationErasesPlaintextFromDatabaseFile(t *testing.T) {
	path := writeLegacyDB(t, true)

	before := dbArtifacts(t, path)
	found := false
	for _, raw := range before {
		if bytes.Contains(raw, []byte(plaintextKey)) {
			found = true
		}
	}
	if !found {
		t.Fatal("the fixture never wrote the plaintext to disk; the test below would prove nothing")
	}

	store, err := NewSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	assertNoPlaintextOnDisk(t, path)
}

// A key created after the migration must never reach the disk in the clear.
func TestCreatedKeyIsNeverWrittenInClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.db")
	store, err := NewSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("new sqlite store: %v", err)
	}

	created, err := store.Create(context.Background(), "fresh", []string{ScopeAdmin}, nil)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	rotated, err := store.RotateKey(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("rotate key: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	for name, raw := range dbArtifacts(t, path) {
		for label, secret := range map[string]string{"created": created.Key, "rotated": rotated.Key} {
			if bytes.Contains(raw, []byte(secret)) {
				t.Errorf("%s holds the %s key in the clear", name, label)
			}
		}
	}
}

// Reopening a migrated database must not re-run the migration or disturb the
// keys it holds.
func TestMigrationIsIdempotentAcrossRestarts(t *testing.T) {
	path := writeLegacyDB(t, true)

	for i := range 3 {
		store, err := NewSQLiteStore(t.Context(), path)
		if err != nil {
			t.Fatalf("open store on start %d: %v", i+1, err)
		}
		if _, ok := store.ValidateKey(context.Background(), plaintextKey); !ok {
			t.Fatalf("key stopped authenticating on start %d", i+1)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("close store on start %d: %v", i+1, err)
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer func() { _ = db.Close() }()

	var applied int
	if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM schema_migrations").Scan(&applied); err != nil {
		t.Fatalf("count applied migrations: %v", err)
	}
	if want := len(keyStoreSteps("sqlite")); applied != want {
		t.Fatalf("schema_migrations holds %d rows, want %d", applied, want)
	}
}

// A fresh database converges on the same schema as a migrated one.
func TestFreshDatabaseMatchesMigratedSchema(t *testing.T) {
	fresh, err := NewSQLiteStore(t.Context(), filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatalf("new sqlite store: %v", err)
	}
	t.Cleanup(func() { _ = fresh.Close() })

	migrated, err := NewSQLiteStore(t.Context(), writeLegacyDB(t, true))
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = migrated.Close() })

	freshCols := columnNames(t, fresh.db, "api_keys")
	migratedCols := columnNames(t, migrated.db, "api_keys")
	if len(freshCols) != len(migratedCols) {
		t.Fatalf("fresh schema %v differs from migrated schema %v", freshCols, migratedCols)
	}
	for i := range freshCols {
		if freshCols[i] != migratedCols[i] {
			t.Fatalf("fresh schema %v differs from migrated schema %v", freshCols, migratedCols)
		}
	}
}

// writeUnmigratableDB builds a legacy database the migration cannot complete:
// two rows share a key, so the rebuild's UNIQUE key_hash constraint rejects the
// copy. The plaintext column is declared without UNIQUE to allow the duplicate.
func writeUnmigratableDB(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "keys.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	defer func() { _ = db.Close() }()

	ddl := `CREATE TABLE api_keys (
		id TEXT PRIMARY KEY, key TEXT NOT NULL, name TEXT NOT NULL, scopes TEXT NOT NULL,
		created_at DATETIME NOT NULL, revoked_at DATETIME NULL, expires_at DATETIME NULL,
		rotated_at DATETIME NULL, active BOOLEAN NOT NULL,
		usage_count INTEGER NOT NULL DEFAULT 0, last_used_at DATETIME NULL);`
	if _, err := db.ExecContext(t.Context(), ddl); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}

	insert := `INSERT INTO api_keys(id, key, name, scopes, created_at, active) VALUES(?, ?, 'legacy', '["admin"]', ?, 1)`
	for _, id := range []string{"first", "second"} {
		if _, err := db.ExecContext(t.Context(), insert, id, plaintextKey, time.Now().UTC()); err != nil {
			t.Fatalf("insert duplicate key: %v", err)
		}
	}
	return path
}

// The migration reads and rewrites every stored secret. If the file is still
// world-readable while that happens, hashing the keys buys nothing.
//
// Asserting the mode after a *successful* open would prove nothing, since a
// chmod that runs afterwards leaves the same final state. So this drives a
// migration that fails partway: the file must already be restricted by the time
// the constructor gives up.
func TestDatabaseIsSecuredBeforeMigrationReadsKeys(t *testing.T) {
	path := writeUnmigratableDB(t)
	// The permissive mode is the precondition under test, not an oversight.
	if err := os.Chmod(path, 0o644); err != nil { //nolint:gosec // G302: deliberately world-readable fixture.
		t.Fatalf("relax file mode: %v", err)
	}

	store, err := NewSQLiteStore(t.Context(), path)
	if err == nil {
		_ = store.Close()
		t.Fatal("the migration was expected to fail on the duplicate key")
	}

	info, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatalf("stat sqlite file: %v", statErr)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("file mode is %o when the migration failed, want 0600: the secrets were read while world-readable", perm)
	}
}

// An "Authorization: Bearer " header with no value hashes to a real digest. It
// must never match, even against a hand-tampered row holding an empty key.
func TestEmptyBearerNeverValidates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.db")
	store, err := NewSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("new sqlite store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	insert := `INSERT INTO api_keys(id, key_hash, key_display, name, scopes, created_at, revoked_at, expires_at, rotated_at, active, usage_count, last_used_at)
VALUES('empty-id', ?, '...', 'empty', '["admin"]', ?, NULL, NULL, NULL, 1, 0, NULL)`
	if _, err := store.db.ExecContext(t.Context(), insert, hashKey(""), time.Now().UTC()); err != nil {
		t.Fatalf("insert degenerate key: %v", err)
	}

	if _, ok := store.ValidateKey(context.Background(), ""); ok {
		t.Fatal("the empty string authenticated against the SQL store")
	}

	mem := NewKeyStore()
	mem.byHash[hashKey("")] = "empty-id"
	mem.byID["empty-id"] = &keyRecord{apiKey: &APIKey{ID: "empty-id", Active: true}, hash: hashKey("")}
	if _, ok := mem.ValidateKey(context.Background(), ""); ok {
		t.Fatal("the empty string authenticated against the in-memory store")
	}
}
