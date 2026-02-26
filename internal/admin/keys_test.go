package admin

import (
	"strings"
	"testing"
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
