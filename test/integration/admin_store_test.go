//go:build integration
// +build integration

package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/admin"
)

func TestPostgresStore_CRUD(t *testing.T) {
	store, err := admin.NewPostgresStore(testDSN)
	if err != nil {
		t.Fatalf("new postgres store: %v", err)
	}
	t.Cleanup(func() { truncateTable(t, "api_keys"); _ = store.Close() })

	created, err := store.Create(context.Background(), "integration-key", []string{admin.ScopeAdmin}, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == "" || created.Key == "" {
		t.Fatal("expected non-empty id and key")
	}

	fetched, ok := store.Get(context.Background(), created.ID)
	if !ok {
		t.Fatal("expected to fetch created key")
	}
	if fetched.ID != created.ID {
		t.Fatalf("get: got %s want %s", fetched.ID, created.ID)
	}

	updated, err := store.Update(context.Background(), created.ID, "updated-name", []string{admin.ScopeReadOnly})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Name != "updated-name" {
		t.Fatalf("expected updated name, got %s", updated.Name)
	}

	if err := store.Delete(context.Background(), created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := store.Get(context.Background(), created.ID); ok {
		t.Fatal("expected key to be deleted")
	}
}

func TestPostgresStore_ValidateAndUsage(t *testing.T) {
	store, err := admin.NewPostgresStore(testDSN)
	if err != nil {
		t.Fatalf("new postgres store: %v", err)
	}
	t.Cleanup(func() { truncateTable(t, "api_keys"); _ = store.Close() })

	created, err := store.Create(context.Background(), "validate-key", []string{admin.ScopeAdmin}, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	validated, valid := store.ValidateKey(context.Background(), created.Key)
	if !valid {
		t.Fatal("expected key to validate")
	}
	if validated.UsageCount != 1 {
		t.Fatalf("expected usage_count 1, got %d", validated.UsageCount)
	}
	if validated.LastUsedAt == nil {
		t.Fatal("expected last_used_at to be set")
	}
}

func TestPostgresStore_Expiration(t *testing.T) {
	store, err := admin.NewPostgresStore(testDSN)
	if err != nil {
		t.Fatalf("new postgres store: %v", err)
	}
	t.Cleanup(func() { truncateTable(t, "api_keys"); _ = store.Close() })

	expired := time.Now().Add(-2 * time.Minute)
	created, err := store.Create(context.Background(), "expired-key", []string{admin.ScopeAdmin}, &expired)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, valid := store.ValidateKey(context.Background(), created.Key); valid {
		t.Fatal("expected expired key to be invalid")
	}

	if err := store.SetExpiration(context.Background(), created.ID, nil); err != nil {
		t.Fatalf("clear expiration: %v", err)
	}
	if _, valid := store.ValidateKey(context.Background(), created.Key); !valid {
		t.Fatal("expected key to validate after clearing expiration")
	}
}

func TestPostgresStore_RevokeAndRotate(t *testing.T) {
	store, err := admin.NewPostgresStore(testDSN)
	if err != nil {
		t.Fatalf("new postgres store: %v", err)
	}
	t.Cleanup(func() { truncateTable(t, "api_keys"); _ = store.Close() })

	created, err := store.Create(context.Background(), "rotate-key", []string{admin.ScopeAdmin}, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	rotated, err := store.RotateKey(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if rotated.Key == created.Key {
		t.Fatal("expected rotated key to differ")
	}
	if _, valid := store.ValidateKey(context.Background(), created.Key); valid {
		t.Fatal("expected old key invalid after rotation")
	}
	if _, valid := store.ValidateKey(context.Background(), rotated.Key); !valid {
		t.Fatal("expected rotated key to validate")
	}

	if err := store.Revoke(context.Background(), created.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, valid := store.ValidateKey(context.Background(), rotated.Key); valid {
		t.Fatal("expected revoked key to be invalid")
	}
}

func TestPostgresStore_ListMasked(t *testing.T) {
	store, err := admin.NewPostgresStore(testDSN)
	if err != nil {
		t.Fatalf("new postgres store: %v", err)
	}
	t.Cleanup(func() { truncateTable(t, "api_keys"); _ = store.Close() })

	for i := range 3 {
		name := "list-key-" + string(rune('a'+i))
		if _, err := store.Create(context.Background(), name, []string{admin.ScopeAdmin}, nil); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}

	listed := store.List(context.Background())
	if len(listed) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(listed))
	}
	for _, k := range listed {
		if !strings.HasSuffix(k.Key, "...") {
			t.Fatalf("expected masked key, got %s", k.Key)
		}
	}
}
