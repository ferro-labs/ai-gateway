package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthMiddleware_ValidKey(t *testing.T) {
	store := NewKeyStore()
	created, _ := store.Create("valid", nil, nil)

	handler := AuthMiddleware(store)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+created.Key)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("got status %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestAuthMiddleware_NoAuthHeader(t *testing.T) {
	store := NewKeyStore()

	handler := AuthMiddleware(store)(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("got status %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestAuthMiddleware_InvalidKey(t *testing.T) {
	store := NewKeyStore()

	handler := AuthMiddleware(store)(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer gw-invalid-key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("got status %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestAuthMiddleware_RevokedKey(t *testing.T) {
	store := NewKeyStore()
	created, _ := store.Create("will-revoke", nil, nil)
	_ = store.Revoke(created.ID)

	handler := AuthMiddleware(store)(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+created.Key)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("got status %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestAuthMiddleware_BootstrapKey(t *testing.T) {
	t.Setenv("ADMIN_BOOTSTRAP_KEY", "bootstrap-secret")
	store := NewKeyStore()

	handler := AuthMiddleware(store)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey, ok := APIKeyFromContext(r.Context())
		if !ok {
			t.Fatal("expected API key in context")
		}
		if len(apiKey.Scopes) != 1 || apiKey.Scopes[0] != ScopeAdmin {
			t.Fatalf("expected admin scope for bootstrap key, got %+v", apiKey.Scopes)
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer bootstrap-secret")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("got status %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestAuthMiddleware_BootstrapKeyMismatch(t *testing.T) {
	t.Setenv("ADMIN_BOOTSTRAP_KEY", "bootstrap-secret")
	store := NewKeyStore()

	handler := AuthMiddleware(store)(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer wrong-secret")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("got status %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestAuthMiddleware_BootstrapReadOnlyKey(t *testing.T) {
	t.Setenv("ADMIN_BOOTSTRAP_READ_ONLY_KEY", "readonly-secret")
	store := NewKeyStore()

	handler := AuthMiddleware(store)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey, ok := APIKeyFromContext(r.Context())
		if !ok {
			t.Fatal("expected API key in context")
		}
		if len(apiKey.Scopes) != 1 || apiKey.Scopes[0] != ScopeReadOnly {
			t.Fatalf("expected read_only scope for bootstrap key, got %+v", apiKey.Scopes)
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer readonly-secret")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("got status %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestAuthMiddleware_BootstrapReadOnlyRequiresScope(t *testing.T) {
	t.Setenv("ADMIN_BOOTSTRAP_READ_ONLY_KEY", "readonly-secret")
	store := NewKeyStore()

	handler := AuthMiddleware(store)(RequireScope(ScopeAdmin)(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("handler should not be called")
	})))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer readonly-secret")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("got status %d, want %d", rr.Code, http.StatusForbidden)
	}
}

func TestAuthMiddleware_BootstrapDisabled(t *testing.T) {
	t.Setenv("ADMIN_BOOTSTRAP_KEY", "bootstrap-secret")
	t.Setenv("ADMIN_BOOTSTRAP_ENABLED", "false")
	store := NewKeyStore()

	handler := AuthMiddleware(store)(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer bootstrap-secret")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("got status %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestAuthMiddleware_BootstrapOnlyWhenStoreEmpty(t *testing.T) {
	t.Setenv("ADMIN_BOOTSTRAP_KEY", "bootstrap-secret")
	store := NewKeyStore()
	_, _ = store.Create("existing-key", []string{ScopeAdmin}, nil)

	handler := AuthMiddleware(store)(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer bootstrap-secret")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("got status %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}
