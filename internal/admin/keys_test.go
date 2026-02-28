package admin

import (
	"strings"
	"testing"
	"time"
)

func TestCreate(t *testing.T) {
	store := NewKeyStore()
	key, err := store.Create("test-key", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(key.Key, "gw-") {
		t.Errorf("key %q does not have gw- prefix", key.Key)
	}
	if !key.Active {
		t.Error("expected key to be active")
	}
	if key.Name != "test-key" {
		t.Errorf("got name %q, want %q", key.Name, "test-key")
	}
	if key.ID == "" {
		t.Error("expected non-empty ID")
	}
}

func TestGet_Existing(t *testing.T) {
	store := NewKeyStore()
	created, _ := store.Create("my-key", nil, nil)

	got, ok := store.Get(created.ID)
	if !ok {
		t.Fatal("expected to find key")
	}
	if got.ID != created.ID {
		t.Errorf("got ID %q, want %q", got.ID, created.ID)
	}
}

func TestGet_NonExisting(t *testing.T) {
	store := NewKeyStore()
	_, ok := store.Get("does-not-exist")
	if ok {
		t.Error("expected key not found")
	}
}

func TestList_KeysMasked(t *testing.T) {
	store := NewKeyStore()
	_, _ = store.Create("key-1", nil, nil)
	_, _ = store.Create("key-2", nil, nil)

	keys := store.List()
	if len(keys) != 2 {
		t.Fatalf("got %d keys, want 2", len(keys))
	}
	for _, k := range keys {
		if !strings.HasSuffix(k.Key, "...") {
			t.Errorf("key %q is not masked", k.Key)
		}
		if len(k.Key) != 11 { // 8 chars + "..."
			t.Errorf("masked key %q has unexpected length %d", k.Key, len(k.Key))
		}
	}
}

func TestRevoke(t *testing.T) {
	store := NewKeyStore()
	created, _ := store.Create("revoke-me", nil, nil)

	if err := store.Revoke(created.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := store.Get(created.ID)
	if !ok {
		t.Fatal("expected to find key")
	}
	if got.Active {
		t.Error("expected key to be inactive")
	}
	if got.RevokedAt == nil {
		t.Error("expected RevokedAt to be set")
	}

	_, valid := store.ValidateKey(created.Key)
	if valid {
		t.Error("expected revoked key to fail validation")
	}
}

func TestDelete(t *testing.T) {
	store := NewKeyStore()
	created, _ := store.Create("delete-me", nil, nil)
	fullKey := created.Key

	if err := store.Delete(created.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, ok := store.Get(created.ID)
	if ok {
		t.Error("expected key to be deleted")
	}

	_, valid := store.ValidateKey(fullKey)
	if valid {
		t.Error("expected deleted key to fail validation")
	}
}

func TestValidateKey_Valid(t *testing.T) {
	store := NewKeyStore()
	created, _ := store.Create("valid-key", nil, nil)

	got, ok := store.ValidateKey(created.Key)
	if !ok {
		t.Fatal("expected key to be valid")
	}
	if got.ID != created.ID {
		t.Errorf("got ID %q, want %q", got.ID, created.ID)
	}
	if got.UsageCount != 1 {
		t.Errorf("expected usage_count 1, got %d", got.UsageCount)
	}
	if got.LastUsedAt == nil {
		t.Error("expected last_used_at to be set")
	}
	if got.LastUsedAt != nil && got.LastUsedAt.Location() != time.UTC {
		t.Errorf("expected last_used_at in UTC, got %v", got.LastUsedAt.Location())
	}
}

func TestValidateKey_IncrementsUsage(t *testing.T) {
	store := NewKeyStore()
	created, _ := store.Create("usage-key", nil, nil)

	_, ok := store.ValidateKey(created.Key)
	if !ok {
		t.Fatal("expected first validation to pass")
	}
	second, ok := store.ValidateKey(created.Key)
	if !ok {
		t.Fatal("expected second validation to pass")
	}
	if second.UsageCount != 2 {
		t.Fatalf("expected usage_count 2, got %d", second.UsageCount)
	}
}

func TestValidateKey_RevokedFails(t *testing.T) {
	store := NewKeyStore()
	created, _ := store.Create("will-revoke", nil, nil)
	_ = store.Revoke(created.ID)

	_, ok := store.ValidateKey(created.Key)
	if ok {
		t.Error("expected revoked key to fail validation")
	}
}

func TestValidateKey_UnknownFails(t *testing.T) {
	store := NewKeyStore()
	_, ok := store.ValidateKey("gw-unknown-key")
	if ok {
		t.Error("expected unknown key to fail validation")
	}
}

func TestSetExpiration_ExpiredFailsValidation(t *testing.T) {
	store := NewKeyStore()
	created, _ := store.Create("expires-soon", nil, nil)

	expiresAt := time.Now().Add(-1 * time.Minute)
	if err := store.SetExpiration(created.ID, &expiresAt); err != nil {
		t.Fatalf("set expiration: %v", err)
	}

	if _, ok := store.ValidateKey(created.Key); ok {
		t.Fatal("expected expired key to fail validation")
	}
}

func TestSetExpiration_ClearAllowsValidation(t *testing.T) {
	store := NewKeyStore()
	expiresAt := time.Now().Add(-1 * time.Minute)
	created, _ := store.Create("expired", nil, &expiresAt)

	if err := store.SetExpiration(created.ID, nil); err != nil {
		t.Fatalf("clear expiration: %v", err)
	}

	if _, ok := store.ValidateKey(created.Key); !ok {
		t.Fatal("expected key to validate after clearing expiration")
	}
}

func TestSetExpiration_StoresUTCCopyWithoutAliasing(t *testing.T) {
	store := NewKeyStore()
	created, _ := store.Create("copy-expiration", nil, nil)

	loc := time.FixedZone("UTC+5", 5*60*60)
	input := time.Date(2026, 2, 28, 10, 30, 0, 0, loc)
	originalInput := input

	if err := store.SetExpiration(created.ID, &input); err != nil {
		t.Fatalf("set expiration: %v", err)
	}

	stored, ok := store.Get(created.ID)
	if !ok {
		t.Fatal("expected key to exist")
	}
	if stored.ExpiresAt == nil {
		t.Fatal("expected expiration to be set")
	}
	if stored.ExpiresAt.Location() != time.UTC {
		t.Fatalf("expected UTC location, got %v", stored.ExpiresAt.Location())
	}

	input = input.Add(24 * time.Hour)
	expectedUTC := originalInput.UTC()
	if !stored.ExpiresAt.Equal(expectedUTC) {
		t.Fatalf("expected stored expiration %v, got %v", expectedUTC, *stored.ExpiresAt)
	}
}
