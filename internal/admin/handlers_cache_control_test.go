package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreateKey_CacheControlNoStore(t *testing.T) {
	h, r := setupTestRouter()
	key := createAdminKey(t, h)

	body := `{"name":"cache-no-store-test"}`
	req := authedRequest(http.MethodPost, "/admin/keys", body, key)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	cc := w.Header().Get("Cache-Control")
	if cc != "no-store" {
		t.Errorf("Cache-Control = %q, want %q", cc, "no-store")
	}
}

// TestRotateKey_CacheControlNoStore verifies that the key-rotate response carries
// Cache-Control: no-store so proxies and browsers cannot cache the new plaintext key.
func TestRotateKey_CacheControlNoStore(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	// Create a second key to rotate.
	target, err := h.Keys.Create(context.Background(), "rotate-target", []string{ScopeAdmin}, nil)
	if err != nil {
		t.Fatalf("create rotate-target key: %v", err)
	}

	req := authedRequest(http.MethodPost, "/admin/keys/"+target.ID+"/rotate", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	cc := w.Header().Get("Cache-Control")
	if cc != "no-store" {
		t.Errorf("Cache-Control = %q, want %q", cc, "no-store")
	}
}
