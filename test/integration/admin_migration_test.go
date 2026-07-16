//go:build integration
// +build integration

package integration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/admin"

	_ "github.com/lib/pq"
)

// migrationSteps is the number of steps in the api_keys migration sequence:
// baseline, hash-and-rebuild, and scrub.
const migrationSteps = 3

// keyStoreLedger is the dedicated ledger the key store records its migration
// versions in (keyStoreLedger in internal/admin/key_migrations.go). Tests that
// reset or count the key store's migrations must target this table, not the
// default schema_migrations — a stale ledger makes the next open skip table
// creation and every later Postgres test fail with "relation does not exist".
const keyStoreLedger = "api_key_schema_migrations"

const legacyPlaintextKey = "fgw_deadbeefcafe0123456789abcdef0123456789abcdef0123456789abcdef01"

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("postgres", testDSN)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close postgres connection: %v", err)
		}
	})
	return db
}

// resetKeySchema removes everything the key store owns so each test starts from
// a known point. It runs on context.Background() because it is used from
// t.Cleanup, where t.Context() is already canceled.
func resetKeySchema(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), "DROP TABLE IF EXISTS api_keys, api_keys_new, schema_migrations, "+keyStoreLedger); err != nil {
		t.Fatalf("reset key schema: %v", err)
	}
}

// seedLegacyKeyTable recreates the api_keys table as v1.1.20 and earlier
// shipped it: the secret in a plaintext UNIQUE column.
func seedLegacyKeyTable(t *testing.T, db *sql.DB) {
	t.Helper()

	ddl := `
CREATE TABLE api_keys (
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
);
CREATE INDEX IF NOT EXISTS idx_api_keys_key ON api_keys(key);`
	if _, err := db.ExecContext(t.Context(), ddl); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}

	insert := `INSERT INTO api_keys(id, key, name, scopes, created_at, active)
VALUES('legacy-id', $1, 'legacy', '["admin"]', $2, true)`
	if _, err := db.ExecContext(t.Context(), insert, legacyPlaintextKey, time.Now().UTC()); err != nil {
		t.Fatalf("insert legacy key: %v", err)
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func columnExists(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	var one int
	err := db.QueryRowContext(t.Context(),
		`SELECT 1 FROM pg_attribute WHERE attrelid = to_regclass($1) AND attname = $2 AND attnum > 0 AND NOT attisdropped`,
		table, column).Scan(&one)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatalf("probe column %s.%s: %v", table, column, err)
	}
	return true
}

func appliedMigrations(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+keyStoreLedger).Scan(&n); err != nil {
		t.Fatalf("count applied migrations: %v", err)
	}
	return n
}

// A Postgres database written by an earlier release is migrated in place: the
// key it holds keeps working, but the secret is replaced by its hash.
func TestPostgresMigration_HashesExistingKeys(t *testing.T) {
	db := openTestDB(t)
	resetKeySchema(t, db)
	seedLegacyKeyTable(t, db)
	t.Cleanup(func() { resetKeySchema(t, db) })

	store, err := admin.NewPostgresStore(t.Context(), testDSN)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close migrated store: %v", err)
		}
	})

	key, ok := store.ValidateKey(context.Background(), legacyPlaintextKey)
	if !ok {
		t.Fatal("the pre-migration key no longer authenticates")
	}
	if key.ID != "legacy-id" {
		t.Fatalf("validated key id = %q, want legacy-id", key.ID)
	}
	if key.Key == legacyPlaintextKey {
		t.Fatal("the store returned the full secret on a read path")
	}

	var storedHash, storedDisplay string
	if err := db.QueryRowContext(t.Context(),
		"SELECT key_hash, key_display FROM api_keys WHERE id = 'legacy-id'").Scan(&storedHash, &storedDisplay); err != nil {
		t.Fatalf("read hashed columns: %v", err)
	}
	if storedHash != sha256Hex(legacyPlaintextKey) {
		t.Fatalf("key_hash = %q, want %q", storedHash, sha256Hex(legacyPlaintextKey))
	}
	if storedDisplay == legacyPlaintextKey {
		t.Fatal("key_display holds the full secret")
	}

	if columnExists(t, db, "api_keys", "key") {
		t.Fatal("the plaintext column survived the migration")
	}

	// The migration must rebuild the table rather than DROP COLUMN. Postgres
	// keeps a dropped column's values in the heap and leaves a tombstone in
	// pg_attribute; a rebuilt table has neither.
	var dropped int
	if err := db.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM pg_attribute WHERE attrelid = to_regclass('api_keys') AND attisdropped`).Scan(&dropped); err != nil {
		t.Fatalf("count dropped columns: %v", err)
	}
	if dropped != 0 {
		t.Fatalf("api_keys has %d dropped-column tombstone(s): the table was altered, not rebuilt", dropped)
	}
}

// Reopening a migrated database must not re-run the migration.
func TestPostgresMigration_IsIdempotent(t *testing.T) {
	db := openTestDB(t)
	resetKeySchema(t, db)
	seedLegacyKeyTable(t, db)
	t.Cleanup(func() { resetKeySchema(t, db) })

	for i := range 3 {
		store, err := admin.NewPostgresStore(t.Context(), testDSN)
		if err != nil {
			t.Fatalf("open store on start %d: %v", i+1, err)
		}
		if _, ok := store.ValidateKey(context.Background(), legacyPlaintextKey); !ok {
			t.Fatalf("key stopped authenticating on start %d", i+1)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("close store on start %d: %v", i+1, err)
		}
	}

	if got := appliedMigrations(t, db); got != migrationSteps {
		t.Fatalf("%s holds %d rows, want %d", keyStoreLedger, got, migrationSteps)
	}
}

// Several gateway instances can share one Postgres database and start at the
// same time. The migration runner holds an advisory lock for its duration, so
// the second instance waits and then finds every step already recorded rather
// than failing against a half-migrated table.
func TestPostgresMigration_ConcurrentStartupsSerialize(t *testing.T) {
	db := openTestDB(t)
	resetKeySchema(t, db)
	seedLegacyKeyTable(t, db)
	t.Cleanup(func() { resetKeySchema(t, db) })

	const instances = 4
	var (
		wg     sync.WaitGroup
		start  = make(chan struct{})
		errs   = make([]error, instances)
		stores = make([]*admin.SQLStore, instances)
	)

	for i := range instances {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			stores[i], errs[i] = admin.NewPostgresStore(context.Background(), testDSN)
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("instance %d failed to start: %v", i, err)
			continue
		}
		t.Cleanup(func() {
			if err := stores[i].Close(); err != nil {
				t.Errorf("close store %d: %v", i, err)
			}
		})
	}
	if t.Failed() {
		return
	}

	if got := appliedMigrations(t, db); got != migrationSteps {
		t.Fatalf("%s holds %d rows, want %d: a step was applied twice", keyStoreLedger, got, migrationSteps)
	}
	if _, ok := stores[0].ValidateKey(context.Background(), legacyPlaintextKey); !ok {
		t.Fatal("the pre-migration key no longer authenticates")
	}
}

// A fresh Postgres database converges on the same schema as a migrated one.
func TestPostgresMigration_FreshDatabaseMatchesMigrated(t *testing.T) {
	db := openTestDB(t)
	resetKeySchema(t, db)
	t.Cleanup(func() { resetKeySchema(t, db) })

	fresh, err := admin.NewPostgresStore(t.Context(), testDSN)
	if err != nil {
		t.Fatalf("open fresh store: %v", err)
	}
	t.Cleanup(func() {
		if err := fresh.Close(); err != nil {
			t.Errorf("close fresh store: %v", err)
		}
	})

	for _, want := range []string{"key_hash", "key_display", "usage_count", "last_used_at"} {
		if !columnExists(t, db, "api_keys", want) {
			t.Errorf("fresh schema is missing column %s", want)
		}
	}
	if columnExists(t, db, "api_keys", "key") {
		t.Error("fresh schema still has the plaintext column")
	}

	created, err := fresh.Create(context.Background(), "fresh", []string{admin.ScopeAdmin}, nil)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}

	var stored string
	if err := db.QueryRowContext(t.Context(), "SELECT key_hash FROM api_keys WHERE id = $1", created.ID).Scan(&stored); err != nil {
		t.Fatalf("read key_hash: %v", err)
	}
	if stored != sha256Hex(created.Key) {
		t.Fatal("a key created after the migration is not stored as its hash")
	}
}
