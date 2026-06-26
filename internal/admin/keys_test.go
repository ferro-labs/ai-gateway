package admin

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCreate(t *testing.T) {
	store := NewKeyStore()
	key, err := store.Create(context.Background(), "test-key", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(key.Key, "fgw_") {
		t.Errorf("key %q does not have fgw_ prefix", key.Key)
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
	created, _ := store.Create(context.Background(), "my-key", nil, nil)

	got, ok := store.Get(context.Background(), created.ID)
	if !ok {
		t.Fatal("expected to find key")
	}
	if got.ID != created.ID {
		t.Errorf("got ID %q, want %q", got.ID, created.ID)
	}
}

func TestGet_NonExisting(t *testing.T) {
	store := NewKeyStore()
	_, ok := store.Get(context.Background(), "does-not-exist")
	if ok {
		t.Error("expected key not found")
	}
}

func TestList_KeysMasked(t *testing.T) {
	store := NewKeyStore()
	_, _ = store.Create(context.Background(), "key-1", nil, nil)
	_, _ = store.Create(context.Background(), "key-2", nil, nil)

	keys := store.List(context.Background())
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
	created, _ := store.Create(context.Background(), "revoke-me", nil, nil)

	if err := store.Revoke(context.Background(), created.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := store.Get(context.Background(), created.ID)
	if !ok {
		t.Fatal("expected to find key")
	}
	if got.Active {
		t.Error("expected key to be inactive")
	}
	if got.RevokedAt == nil {
		t.Error("expected RevokedAt to be set")
	}

	_, valid := store.ValidateKey(context.Background(), created.Key)
	if valid {
		t.Error("expected revoked key to fail validation")
	}
}

func TestDelete(t *testing.T) {
	store := NewKeyStore()
	created, _ := store.Create(context.Background(), "delete-me", nil, nil)
	fullKey := created.Key

	if err := store.Delete(context.Background(), created.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, ok := store.Get(context.Background(), created.ID)
	if ok {
		t.Error("expected key to be deleted")
	}

	_, valid := store.ValidateKey(context.Background(), fullKey)
	if valid {
		t.Error("expected deleted key to fail validation")
	}
}

func TestValidateKey_Valid(t *testing.T) {
	store := NewKeyStore()
	created, _ := store.Create(context.Background(), "valid-key", nil, nil)

	got, ok := store.ValidateKey(context.Background(), created.Key)
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
	created, _ := store.Create(context.Background(), "usage-key", nil, nil)

	_, ok := store.ValidateKey(context.Background(), created.Key)
	if !ok {
		t.Fatal("expected first validation to pass")
	}
	second, ok := store.ValidateKey(context.Background(), created.Key)
	if !ok {
		t.Fatal("expected second validation to pass")
	}
	if second.UsageCount != 2 {
		t.Fatalf("expected usage_count 2, got %d", second.UsageCount)
	}
}

func TestValidateKey_RevokedFails(t *testing.T) {
	store := NewKeyStore()
	created, _ := store.Create(context.Background(), "will-revoke", nil, nil)
	_ = store.Revoke(context.Background(), created.ID)

	_, ok := store.ValidateKey(context.Background(), created.Key)
	if ok {
		t.Error("expected revoked key to fail validation")
	}
}

func TestValidateKey_UnknownFails(t *testing.T) {
	store := NewKeyStore()
	_, ok := store.ValidateKey(context.Background(), "gw-unknown-key")
	if ok {
		t.Error("expected unknown key to fail validation")
	}
}

func TestSetExpiration_ExpiredFailsValidation(t *testing.T) {
	store := NewKeyStore()
	created, _ := store.Create(context.Background(), "expires-soon", nil, nil)

	expiresAt := time.Now().Add(-1 * time.Minute)
	if err := store.SetExpiration(context.Background(), created.ID, &expiresAt); err != nil {
		t.Fatalf("set expiration: %v", err)
	}

	if _, ok := store.ValidateKey(context.Background(), created.Key); ok {
		t.Fatal("expected expired key to fail validation")
	}
}

func TestSetExpiration_ClearAllowsValidation(t *testing.T) {
	store := NewKeyStore()
	expiresAt := time.Now().Add(-1 * time.Minute)
	created, _ := store.Create(context.Background(), "expired", nil, &expiresAt)

	if err := store.SetExpiration(context.Background(), created.ID, nil); err != nil {
		t.Fatalf("clear expiration: %v", err)
	}

	if _, ok := store.ValidateKey(context.Background(), created.Key); !ok {
		t.Fatal("expected key to validate after clearing expiration")
	}
}

func TestSetExpiration_StoresUTCCopyWithoutAliasing(t *testing.T) {
	store := NewKeyStore()
	created, _ := store.Create(context.Background(), "copy-expiration", nil, nil)

	loc := time.FixedZone("UTC+5", 5*60*60)
	input := time.Date(2026, 2, 28, 10, 30, 0, 0, loc)
	originalInput := input

	if err := store.SetExpiration(context.Background(), created.ID, &input); err != nil {
		t.Fatalf("set expiration: %v", err)
	}

	stored, ok := store.Get(context.Background(), created.ID)
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

func TestGet_ReturnsDefensiveCopy(t *testing.T) {
	store := NewKeyStore()
	expiresAt := time.Now().Add(time.Hour)
	created, _ := store.Create(context.Background(), "copy-me", []string{ScopeReadOnly}, &expiresAt)

	got, ok := store.Get(context.Background(), created.ID)
	if !ok {
		t.Fatal("expected key to exist")
	}
	got.Name = "mutated"
	got.Scopes[0] = ScopeAdmin
	*got.ExpiresAt = got.ExpiresAt.Add(time.Hour)

	again, ok := store.Get(context.Background(), created.ID)
	if !ok {
		t.Fatal("expected key to exist")
	}
	if again.Name != "copy-me" {
		t.Fatalf("stored name = %q, want copy-me", again.Name)
	}
	if again.Scopes[0] != ScopeReadOnly {
		t.Fatalf("stored scope = %q, want %q", again.Scopes[0], ScopeReadOnly)
	}
	if !again.ExpiresAt.Equal(expiresAt.UTC()) {
		t.Fatalf("stored expiration = %v, want %v", *again.ExpiresAt, expiresAt.UTC())
	}
}

func TestValidateKey_ReturnsDefensiveCopy(t *testing.T) {
	store := NewKeyStore()
	created, _ := store.Create(context.Background(), "validate-copy", []string{ScopeReadOnly}, nil)

	validated, ok := store.ValidateKey(context.Background(), created.Key)
	if !ok {
		t.Fatal("expected key to validate")
	}
	validated.UsageCount = 100
	validated.LastUsedAt = nil
	validated.Scopes[0] = ScopeAdmin

	stored, ok := store.Get(context.Background(), created.ID)
	if !ok {
		t.Fatal("expected key to exist")
	}
	if stored.UsageCount != 1 {
		t.Fatalf("stored usage count = %d, want 1", stored.UsageCount)
	}
	if stored.LastUsedAt == nil {
		t.Fatal("stored last-used timestamp was cleared through returned key")
	}
	if stored.Scopes[0] != ScopeReadOnly {
		t.Fatalf("stored scope = %q, want %q", stored.Scopes[0], ScopeReadOnly)
	}
}
