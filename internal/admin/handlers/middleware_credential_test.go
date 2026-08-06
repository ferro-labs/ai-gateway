package handlers

import (
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/admin/model"
	"github.com/ferro-labs/ai-gateway/internal/admin/repository"
)

func TestCredentialValidator(t *testing.T) {
	ctx := t.Context()
	store := repository.NewKeyStore()
	key, err := store.Create(ctx, "worker", []string{model.ScopeReadOnly}, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	validate, _ := NewCredentialValidator(store, "master-secret")

	t.Run("master key resolves to the master identity", func(t *testing.T) {
		got, ok := validate(ctx, "master-secret")
		if !ok {
			t.Fatal("master key rejected")
		}
		if want := masterCredentialID("master-secret"); got.ID != want {
			t.Fatalf("ID = %q, want %q", got.ID, want)
		}
		if len(got.Scopes) != 1 || got.Scopes[0] != model.ScopeAdmin {
			t.Fatalf("scopes = %v, want [admin]", got.Scopes)
		}
	})

	t.Run("stored key resolves with its own scopes", func(t *testing.T) {
		got, ok := validate(ctx, key.Key)
		if !ok {
			t.Fatal("stored key rejected")
		}
		if len(got.Scopes) != 1 || got.Scopes[0] != model.ScopeReadOnly {
			t.Fatalf("scopes = %v, want [read_only]", got.Scopes)
		}
	})

	t.Run("unknown credential is rejected", func(t *testing.T) {
		if _, ok := validate(ctx, "nonsense"); ok {
			t.Fatal("unknown credential accepted")
		}
	})

	t.Run("empty credential is rejected", func(t *testing.T) {
		if _, ok := validate(ctx, ""); ok {
			t.Fatal("empty credential accepted")
		}
	})

	t.Run("master key wins over an identical stored key", func(t *testing.T) {
		s := repository.NewKeyStore()
		k, err := s.Create(ctx, "collide", []string{model.ScopeReadOnly}, nil)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		collideValidate, _ := NewCredentialValidator(s, k.Key)
		got, ok := collideValidate(ctx, k.Key)
		if !ok {
			t.Fatal("credential rejected")
		}
		if want := masterCredentialID(k.Key); got.ID != want {
			t.Fatalf("ID = %q, want %q: the store was consulted before the master key", got.ID, want)
		}
	})
}
