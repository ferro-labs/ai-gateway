package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/admin/model"
	"github.com/ferro-labs/ai-gateway/internal/admin/repository"
	"github.com/ferro-labs/ai-gateway/internal/authctx"
)

func TestAuthMiddleware_ValidKey(t *testing.T) {
	store := repository.NewKeyStore()
	created, _ := store.Create(context.Background(), "valid", nil, nil)

	handler := AuthMiddleware(store, "")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+created.Key)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("got status %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestAuthMiddleware_NoAuthHeader(t *testing.T) {
	store := repository.NewKeyStore()

	handler := AuthMiddleware(store, "")(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("handler should not be called")
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("got status %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestAuthMiddleware_InvalidKey(t *testing.T) {
	store := repository.NewKeyStore()

	handler := AuthMiddleware(store, "")(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("handler should not be called")
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer gw-invalid-key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("got status %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestAuthMiddleware_RevokedKey(t *testing.T) {
	store := repository.NewKeyStore()
	created, _ := store.Create(context.Background(), "will-revoke", nil, nil)
	_ = store.Revoke(context.Background(), created.ID)

	handler := AuthMiddleware(store, "")(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("handler should not be called")
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+created.Key)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("got status %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestAuthMiddleware_MasterKey(t *testing.T) {
	store := repository.NewKeyStore()
	handler := AuthMiddleware(store, "test-master-key")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, ok := model.APIKeyFromContext(r.Context())
		if !ok {
			t.Fatal("expected API key in context")
		}
		if key.ID != masterCredentialID("test-master-key") {
			t.Errorf("expected master-key ID, got %s", key.ID)
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/admin/keys", nil)
	req.Header.Set("Authorization", "Bearer test-master-key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("master key should authenticate, got %d", rr.Code)
	}
}

func TestAuthMiddleware_MasterKey_WrongKey(t *testing.T) {
	store := repository.NewKeyStore()
	handler := AuthMiddleware(store, "test-master-key")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/admin/keys", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("wrong key should get 401, got %d", rr.Code)
	}
}

func TestAuthMiddleware_MasterKey_Empty(t *testing.T) {
	store := repository.NewKeyStore()
	// Empty master key — should fall through to store lookup.
	handler := AuthMiddleware(store, "")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/admin/keys", nil)
	req.Header.Set("Authorization", "Bearer some-key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	// No master key, no stored keys → 401.
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with no master key and no stored keys, got %d", rr.Code)
	}
}

// TestAuthMiddleware_BootstrapEnvVarsAreNotCredentials pins the removal of the
// ADMIN_BOOTSTRAP_* path. It sets up the exact condition that path used to
// admit — no master key, a key store holding no admin key, the bootstrap
// variables set — and requires a 401 for every one of them.
//
// The path was a credential that re-opened whenever the admin-key count fell
// back to zero, which is the shape behind Rancher CVE-2019-11202 and Portainer
// CVE-2026-55761. MASTER_KEY covers both jobs it did (bootstrap and
// break-glass) without a re-opening window, and every other test in this file
// exercises it.
func TestAuthMiddleware_BootstrapEnvVarsAreNotCredentials(t *testing.T) {
	for _, name := range []string{"ADMIN_BOOTSTRAP_KEY", "ADMIN_BOOTSTRAP_READ_ONLY_KEY"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "bootstrap-secret")
			t.Setenv("ADMIN_BOOTSTRAP_ENABLED", "true")

			handler := AuthMiddleware(repository.NewKeyStore(), "")(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
				t.Error("handler should not be called")
			}))

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/admin/keys", nil)
			req.Header.Set("Authorization", "Bearer bootstrap-secret")
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401: %s still authenticates", rr.Code, name)
			}
		})
	}
}

// TestAuthMiddlewareWithSessions_BucketsUnderSourceCredential pins the fix
// for the session budget/rate-limit bypass: gateway.go reads
// authctx.KeyID(ctx) into pctx.Metadata["api_key"], which is the bucket key
// the budget and rate-limit plugins scope their limits by. A session must
// bucket under the credential it was minted from, not under its own session
// ID -- otherwise a caller holding a key with a $10 budget or a per-key RPM
// cap could mint a session and receive a fresh, empty bucket, and mint again
// for another.
//
// Asserted via authctx.KeyID(ctx) after the middleware runs: that is exactly
// the value gateway.go reads, and the layer closest to the actual bypass.
// APIKeyFromContext(ctx).ID is asserted too, to pin that it must NOT change:
// guardKeyMutation and deleteSession depend on the "session:" prefix there.
func TestAuthMiddlewareWithSessions_BucketsUnderSourceCredential(t *testing.T) {
	store := repository.NewKeyStore()
	sessions := repository.NewSessionStore()

	created, err := store.Create(context.Background(), "budgeted-key", nil, nil)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}

	sess, token, err := sessions.CreateSession(context.Background(), created.Name, created.ID, created.Scopes, repository.DefaultSessionTTL)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	var gotBucketID, gotAPIKeyID string
	handler := AuthMiddlewareWithSessions(store, sessions, "")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBucketID, _ = authctx.KeyID(r.Context())
		if key, ok := model.APIKeyFromContext(r.Context()); ok {
			gotAPIKeyID = key.ID
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if gotBucketID != created.ID {
		t.Errorf("authctx bucket id = %q, want the source credential id %q (session id was %q) -- "+
			"a session must bucket budget/rate-limit under its source credential, not a fresh id of its own",
			gotBucketID, created.ID, sess.ID)
	}
	if gotAPIKeyID != repository.SessionIdentityPrefix+sess.ID {
		t.Errorf("APIKeyFromContext id = %q, want %q -- guardKeyMutation's self-mutation check "+
			"depends on the session prefix surviving unchanged", gotAPIKeyID, repository.SessionIdentityPrefix+sess.ID)
	}
}

// TestAuthMiddlewareWithSessions_SyntheticCredentialBucketsUnderSameID covers
// the synthetic master identity: it has no store row, so Session.CredentialID
// for a session minted from it already IS the synthetic ID
// (e.g. "master-key:<fingerprint>") rather than a lookup result. A session
// minted from the master key must bucket under that same synthetic id,
// exactly as authenticating with the master key directly does today.
func TestAuthMiddlewareWithSessions_SyntheticCredentialBucketsUnderSameID(t *testing.T) {
	store := repository.NewKeyStore()
	sessions := repository.NewSessionStore()
	const masterKey = "test-master-key"

	validate, _ := NewCredentialValidator(store, masterKey)
	identity, ok := validate(context.Background(), masterKey)
	if !ok {
		t.Fatal("master key failed to validate")
	}

	_, token, err := sessions.CreateSession(context.Background(), identity.Name, identity.ID, identity.Scopes, repository.DefaultSessionTTL)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	var gotBucketID string
	handler := AuthMiddlewareWithSessions(store, sessions, masterKey)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBucketID, _ = authctx.KeyID(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if want := masterCredentialID(masterKey); gotBucketID != want {
		t.Errorf("authctx bucket id = %q, want synthetic master key id %q", gotBucketID, want)
	}
}

// A session minted from the master key is bound to that key's value, not to
// the master role. Rotating MASTER_KEY after a leak, or unsetting it, is the
// emergency control an operator reaches for, so it has to cut off the sessions
// minted from the old value on the next request rather than leaving them
// admin-scoped for the rest of their 24h lifetime — which is what a persistent
// session backend would otherwise carry across the restart.
func TestAuthMiddlewareWithSessions_MasterKeySessionBoundToKeyValue(t *testing.T) {
	const originalMaster = "test-master-key"

	tests := []struct {
		name        string
		restartWith string
		wantStatus  int
	}{
		{"unchanged master key keeps the session live", originalMaster, http.StatusOK},
		{"rotated master key invalidates the session", "rotated-master-key", http.StatusUnauthorized},
		{"unset master key invalidates the session", "", http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := repository.NewKeyStore()
			sessions := repository.NewSessionStore()

			validate, _ := NewCredentialValidator(store, originalMaster)
			identity, ok := validate(t.Context(), originalMaster)
			if !ok {
				t.Fatal("master key failed to validate")
			}
			_, token, err := sessions.CreateSession(t.Context(), identity.Name, identity.ID, identity.Scopes, repository.DefaultSessionTTL)
			if err != nil {
				t.Fatalf("create session: %v", err)
			}

			// The session store survives; only the configured master key changes.
			handler := AuthMiddlewareWithSessions(store, sessions, tt.restartWith)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/admin/keys", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d: %s", rr.Code, tt.wantStatus, rr.Body.String())
			}
		})
	}
}

// Rotating the master key must not reach beyond the sessions it minted: a
// session from a stored API key is re-checked against that key's row and is
// unaffected by what MASTER_KEY holds.
func TestAuthMiddlewareWithSessions_StoredKeySessionSurvivesMasterKeyRotation(t *testing.T) {
	store := repository.NewKeyStore()
	sessions := repository.NewSessionStore()

	created, err := store.Create(t.Context(), "operator", []string{model.ScopeAdmin}, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, token, err := sessions.CreateSession(t.Context(), created.Name, created.ID, created.Scopes, repository.DefaultSessionTTL)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	handler := AuthMiddlewareWithSessions(store, sessions, "rotated-master-key")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/admin/keys", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
}
