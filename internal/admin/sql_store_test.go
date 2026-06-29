package admin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSQLiteStoreImplementsStore(_ *testing.T) {
	var _ Store = (*SQLStore)(nil)
}

func TestSQLiteStoreContract(t *testing.T) {
	store := newSQLiteTestStore(t)
	runStoreContract(t, store)
}

func TestPostgresStoreContract(t *testing.T) {
	dsn := os.Getenv("FERROGW_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set FERROGW_TEST_POSTGRES_DSN to run Postgres store integration tests")
	}

	store, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatalf("new postgres store: %v", err)
	}
	t.Cleanup(func() {
		_, _ = store.db.Exec("DELETE FROM api_keys")
		if store.db != nil {
			_ = store.db.Close()
		}
	})

	_, _ = store.db.Exec("DELETE FROM api_keys")
	runStoreContract(t, store)
}

func runStoreContract(t *testing.T, store Store) {
	t.Helper()

	created, err := store.Create(context.Background(), "store-key", []string{ScopeAdmin}, nil)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	if created.ID == "" || created.Key == "" {
		t.Fatalf("expected created key to have id and key")
	}

	fetched, ok := store.Get(context.Background(), created.ID)
	if !ok {
		t.Fatalf("expected to fetch created key")
	}
	if fetched.ID != created.ID {
		t.Fatalf("get returned wrong key id: got %s want %s", fetched.ID, created.ID)
	}
	if fetched.UsageCount != 0 {
		t.Fatalf("expected initial usage_count 0, got %d", fetched.UsageCount)
	}

	validated, valid := store.ValidateKey(context.Background(), created.Key)
	if !valid {
		t.Fatalf("expected created key to validate")
	}
	if validated.UsageCount != 1 {
		t.Fatalf("expected usage_count 1 after validate, got %d", validated.UsageCount)
	}
	if validated.LastUsedAt == nil {
		t.Fatalf("expected last_used_at to be set after validate")
	}

	listed := store.List(context.Background())
	if len(listed) != 1 {
		t.Fatalf("expected 1 key in list, got %d", len(listed))
	}
	if !strings.HasSuffix(listed[0].Key, "...") {
		t.Fatalf("expected listed key to be masked, got %s", listed[0].Key)
	}

	updated, err := store.Update(context.Background(), created.ID, "store-key-updated", []string{ScopeReadOnly})
	if err != nil {
		t.Fatalf("update key: %v", err)
	}
	if updated.Name != "store-key-updated" {
		t.Fatalf("expected updated name, got %s", updated.Name)
	}
	if len(updated.Scopes) != 1 || updated.Scopes[0] != ScopeReadOnly {
		t.Fatalf("expected updated scopes, got %v", updated.Scopes)
	}

	expiresAt := time.Now().Add(-1 * time.Minute)
	if err := store.SetExpiration(context.Background(), created.ID, &expiresAt); err != nil {
		t.Fatalf("set expiration: %v", err)
	}
	if _, valid := store.ValidateKey(context.Background(), created.Key); valid {
		t.Fatalf("expected expired key to be invalid")
	}
	if err := store.SetExpiration(context.Background(), created.ID, nil); err != nil {
		t.Fatalf("clear expiration: %v", err)
	}
	if _, valid := store.ValidateKey(context.Background(), created.Key); !valid {
		t.Fatalf("expected key to validate after clearing expiration")
	}

	rotated, err := store.RotateKey(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("rotate key: %v", err)
	}
	if rotated.Key == created.Key {
		t.Fatalf("expected rotated key to change")
	}

	if _, valid := store.ValidateKey(context.Background(), created.Key); valid {
		t.Fatalf("expected old key to be invalid after rotation")
	}
	if _, valid := store.ValidateKey(context.Background(), rotated.Key); !valid {
		t.Fatalf("expected rotated key to validate")
	}

	if err := store.Revoke(context.Background(), created.ID); err != nil {
		t.Fatalf("revoke key: %v", err)
	}
	if _, valid := store.ValidateKey(context.Background(), rotated.Key); valid {
		t.Fatalf("expected revoked key to be invalid")
	}

	if err := store.Delete(context.Background(), created.ID); err != nil {
		t.Fatalf("delete key: %v", err)
	}
	if _, ok := store.Get(context.Background(), created.ID); ok {
		t.Fatalf("expected key deleted")
	}
}

func TestSQLiteStoreExpiration(t *testing.T) {
	store := newSQLiteTestStore(t)

	expiresAt := time.Now().Add(-2 * time.Minute)
	created, err := store.Create(context.Background(), "expired", []string{ScopeAdmin}, &expiresAt)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}

	if _, valid := store.ValidateKey(context.Background(), created.Key); valid {
		t.Fatalf("expected expired key to be invalid")
	}
}

func TestPostgresStoreMissingDSN(t *testing.T) {
	if _, err := NewPostgresStore(""); err == nil {
		t.Fatalf("expected error for missing postgres dsn")
	}
}

func newSQLiteTestStore(t *testing.T) *SQLStore {
	t.Helper()

	path := filepath.Join(t.TempDir(), "keys.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("new sqlite store: %v", err)
	}
	t.Cleanup(func() {
		if store.db != nil {
			_ = store.db.Close()
		}
		_ = os.Remove(path)
	})

	return store
}

// TestSQLStore_RespectsCancelledContext guards that request-context cancellation
// propagates through ExecContext/QueryContext to the underlying SQLite driver.
// Create returns an error (Get/ValidateKey only return bool), so it is the
// method that surfaces context.Canceled for assertion.
func TestSQLStore_RespectsCancelledContext(t *testing.T) {
	store := newSQLiteTestStore(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before any work runs

	_, err := store.Create(ctx, "cancelled", []string{ScopeAdmin}, nil)
	if err == nil {
		t.Fatal("expected error from Create with cancelled context, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
}
