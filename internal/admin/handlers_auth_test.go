package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthCheck(t *testing.T) {
	h, r := setupTestRouter()
	key := createAdminKey(t, h)

	req := authedRequest(http.MethodGet, "/admin/health", "", key)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// 200 even while degraded: the dashboard login probe and providers page read
	// any non-2xx here as an auth failure. The body carries the real status.
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result map[string]any
	decodeJSON(t, w.Body, &result)
	if result["status"] != "no_providers" {
		t.Fatalf("expected status no_providers, got %v", result["status"])
	}
	if _, ok := result["providers"]; !ok {
		t.Error("expected providers field")
	}
}

func TestRBACReadOnlyCannotCreateKey(t *testing.T) {
	h, r := setupTestRouter()
	// Create an admin key first to bootstrap, then create a read-only key.
	adminKey := createAdminKey(t, h)
	roKey := createTestKey(t, h, "ro-key", []string{ScopeReadOnly}, nil)

	// Read-only key should be able to list keys.
	req := authedRequest(http.MethodGet, "/admin/keys", "", roKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected read-only to list keys (200), got %d", w.Code)
	}

	// Read-only key should NOT be able to create keys.
	body := `{"name":"should-fail"}`
	req = authedRequest(http.MethodPost, "/admin/keys", body, roKey)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected read-only create to fail (403), got %d", w.Code)
	}

	// Admin key should be able to create keys.
	req = authedRequest(http.MethodPost, "/admin/keys", body, adminKey)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected admin create to succeed (201), got %d: %s", w.Code, w.Body.String())
	}
}

func TestUnauthorizedRequest(t *testing.T) {
	_, r := setupTestRouter()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/admin/keys", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestReadOnlyCannotUpdateConfig(t *testing.T) {
	h, r := setupTestRouter()
	createAdminKey(t, h)
	roKey := createReadOnlyKey(t, h)

	body := `{"strategy":{"mode":"single"},"targets":[{"virtual_key":"openai"}]}`
	req := authedRequest(http.MethodPut, "/admin/config", body, roKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected read-only config update to fail (403), got %d", w.Code)
	}
}
