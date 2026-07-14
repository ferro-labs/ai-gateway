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
	if key.CreatedAt.IsZero() || key.CreatedAt.Location() != time.UTC {
		t.Errorf("expected non-zero created_at in UTC, got %v", key.CreatedAt)
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
	first, err := store.Create(context.Background(), "key-1", nil, nil)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	_, _ = store.Create(context.Background(), "key-2", nil, nil)

	keys := store.List(context.Background())
	if len(keys) != 2 {
		t.Fatalf("got %d keys, want 2", len(keys))
	}
	for _, k := range keys {
		if k.Key == first.Key {
			t.Errorf("List returned the full secret %q", k.Key)
		}
		if !strings.Contains(k.Key, "...") {
			t.Errorf("key %q is not a display form", k.Key)
		}
		if len(k.Key) != keyDisplayHead+3+keyDisplayTail {
			t.Errorf("display key %q has unexpected length %d", k.Key, len(k.Key))
		}
	}
}

// The display form keeps both ends of the secret so an operator can match a key
// they hold against a stored record.
func TestDisplayKeyKeepsBothEnds(t *testing.T) {
	const key = "fgw_0123456789abcdef0123456789abcdef"
	if got, want := displayKey(key), "fgw_0123...cdef"; got != want {
		t.Fatalf("displayKey = %q, want %q", got, want)
	}
	if got := displayKey("fgw_short"); got != "..." {
		t.Fatalf("displayKey of a short value = %q, want %q", got, "...")
	}
}

// ValidateKey must accept the plaintext even though only its hash is stored,
// and reject anything else.
func TestValidateKeyMatchesOnHash(t *testing.T) {
	store := NewKeyStore()
	created, err := store.Create(context.Background(), "key", nil, nil)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}

	if _, ok := store.ValidateKey(context.Background(), created.Key); !ok {
		t.Fatal("the plaintext returned by Create does not validate")
	}
	if _, ok := store.ValidateKey(context.Background(), hashKey(created.Key)); ok {
		t.Fatal("the stored hash was accepted as a bearer token")
	}
	if _, ok := store.ValidateKey(context.Background(), displayKey(created.Key)); ok {
		t.Fatal("the display form was accepted as a bearer token")
	}
}

func TestIsEmpty(t *testing.T) {
	store := NewKeyStore()
	empty, err := store.IsEmpty(context.Background())
	if err != nil || !empty {
		t.Fatalf("IsEmpty on a new store = (%v, %v), want (true, nil)", empty, err)
	}

	created, err := store.Create(context.Background(), "key", nil, nil)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	if empty, _ := store.IsEmpty(context.Background()); empty {
		t.Fatal("IsEmpty is true with a key present")
	}

	if err := store.Delete(context.Background(), created.ID); err != nil {
		t.Fatalf("delete key: %v", err)
	}
	if empty, _ := store.IsEmpty(context.Background()); !empty {
		t.Fatal("IsEmpty is false after the only key was deleted")
	}
}

// Delete and RotateKey both retire the old hash; a stale entry would leave the
// previous secret usable.
func TestRotateAndDeleteRetireTheOldHash(t *testing.T) {
	store := NewKeyStore()
	created, err := store.Create(context.Background(), "key", nil, nil)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}

	rotated, err := store.RotateKey(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("rotate key: %v", err)
	}
	if _, ok := store.ValidateKey(context.Background(), created.Key); ok {
		t.Fatal("the pre-rotation secret still validates")
	}
	if _, ok := store.ValidateKey(context.Background(), rotated.Key); !ok {
		t.Fatal("the rotated secret does not validate")
	}
	if rotated.RotatedAt == nil || rotated.RotatedAt.Location() != time.UTC {
		t.Fatalf("expected rotated_at in UTC, got %v", rotated.RotatedAt)
	}

	if err := store.Delete(context.Background(), created.ID); err != nil {
		t.Fatalf("delete key: %v", err)
	}
	if _, ok := store.ValidateKey(context.Background(), rotated.Key); ok {
		t.Fatal("a deleted key still validates")
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
	if got.RevokedAt != nil && got.RevokedAt.Location() != time.UTC {
		t.Errorf("expected revoked_at in UTC, got %v", got.RevokedAt.Location())
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
