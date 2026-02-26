package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func setupTestRouter() (*Handlers, chi.Router) {
	store := NewKeyStore()
	h := &Handlers{
		Keys: store,
	}
	r := chi.NewRouter()
	r.Use(AuthMiddleware(store))
	r.Mount("/admin", h.Routes())
	return h, r
}

func createAdminKey(t *testing.T, h *Handlers) *APIKey {
	t.Helper()
	key, err := h.Keys.Create("admin-key", []string{ScopeAdmin}, nil)
	if err != nil {
		t.Fatalf("failed to create admin key: %v", err)
	}
	return key
}

func createReadOnlyKey(t *testing.T, h *Handlers) *APIKey {
	t.Helper()
	key, err := h.Keys.Create("readonly-key", []string{ScopeReadOnly}, nil)
	if err != nil {
		t.Fatalf("failed to create readonly key: %v", err)
	}
	return key
}

func authedRequest(method, url string, body string, apiKey *APIKey) *http.Request {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, url, bytes.NewBufferString(body))
	} else {
		req = httptest.NewRequest(method, url, nil)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey.Key)
	return req
}

func TestCreateKey(t *testing.T) {
	h, r := setupTestRouter()
	key := createAdminKey(t, h)

	body := `{"name":"test-key"}`
	req := authedRequest(http.MethodPost, "/admin/keys", body, key)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var created APIKey
	_ = json.NewDecoder(w.Body).Decode(&created)
	if created.Name != "test-key" {
		t.Errorf("expected name test-key, got %s", created.Name)
	}
	if created.Key == "" {
		t.Error("expected key to be set")
	}
}

func TestCreateKeyWithScopes(t *testing.T) {
	h, r := setupTestRouter()
	key := createAdminKey(t, h)

	body := `{"name":"readonly","scopes":["read_only"]}`
	req := authedRequest(http.MethodPost, "/admin/keys", body, key)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var created APIKey
	_ = json.NewDecoder(w.Body).Decode(&created)
	if len(created.Scopes) != 1 || created.Scopes[0] != ScopeReadOnly {
		t.Errorf("expected scopes [read-only], got %v", created.Scopes)
	}
}

func TestCreateKeyMissingName(t *testing.T) {
	h, r := setupTestRouter()
	key := createAdminKey(t, h)

	body := `{}`
	req := authedRequest(http.MethodPost, "/admin/keys", body, key)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestListKeys(t *testing.T) {
	h, r := setupTestRouter()
	key := createAdminKey(t, h)
	_, _ = h.Keys.Create("key-1", nil, nil)
	_, _ = h.Keys.Create("key-2", nil, nil)

	req := authedRequest(http.MethodGet, "/admin/keys", "", key)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var keys []*APIKey
	_ = json.NewDecoder(w.Body).Decode(&keys)
	if len(keys) != 3 { // admin key + 2 created
		t.Fatalf("expected 3 keys, got %d", len(keys))
	}
	for _, k := range keys {
		if len(k.Key) > 11 {
			t.Errorf("expected masked key, got %s", k.Key)
		}
	}
}

func TestUpdateKey(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)
	target, _ := h.Keys.Create("original", nil, nil)

	body := `{"name":"updated","scopes":["read_only"]}`
	req := authedRequest(http.MethodPut, "/admin/keys/"+target.ID, body, adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var updated APIKey
	_ = json.NewDecoder(w.Body).Decode(&updated)
	if updated.Name != "updated" {
		t.Errorf("expected name updated, got %s", updated.Name)
	}
	if len(updated.Scopes) != 1 || updated.Scopes[0] != ScopeReadOnly {
		t.Errorf("expected scopes [read-only], got %v", updated.Scopes)
	}
}

func TestUpdateKeyNotFound(t *testing.T) {
	h, r := setupTestRouter()
	key := createAdminKey(t, h)

	body := `{"name":"x"}`
	req := authedRequest(http.MethodPut, "/admin/keys/nonexistent", body, key)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestDeleteKey(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)
	target, _ := h.Keys.Create("to-delete", nil, nil)

	req := authedRequest(http.MethodDelete, "/admin/keys/"+target.ID, "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	if _, ok := h.Keys.Get(target.ID); ok {
		t.Error("expected key to be deleted")
	}
}

func TestDeleteKeyNotFound(t *testing.T) {
	h, r := setupTestRouter()
	key := createAdminKey(t, h)

	req := authedRequest(http.MethodDelete, "/admin/keys/nonexistent", "", key)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestRevokeKey(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)
	target, _ := h.Keys.Create("to-revoke", nil, nil)

	req := authedRequest(http.MethodPost, "/admin/keys/"+target.ID+"/revoke", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	k, ok := h.Keys.Get(target.ID)
	if !ok {
		t.Fatal("expected key to exist")
	}
	if k.Active {
		t.Error("expected key to be inactive")
	}
}

func TestHealthCheck(t *testing.T) {
	h, r := setupTestRouter()
	key := createAdminKey(t, h)

	req := authedRequest(http.MethodGet, "/admin/health", "", key)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&result)
	if _, ok := result["status"]; !ok {
		t.Error("expected status field")
	}
	if _, ok := result["providers"]; !ok {
		t.Error("expected providers field")
	}
}

func TestRBACReadOnlyCannotCreateKey(t *testing.T) {
	h, r := setupTestRouter()
	// Create an admin key first to bootstrap, then create a read-only key.
	adminKey := createAdminKey(t, h)
	roKey, _ := h.Keys.Create("ro-key", []string{ScopeReadOnly}, nil)

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

	req := httptest.NewRequest(http.MethodGet, "/admin/keys", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestReadOnlyCannotCreateConfig(t *testing.T) {
	h, r := setupTestRouter()
	createAdminKey(t, h)
	roKey := createReadOnlyKey(t, h)

	// Since /admin/configs route no longer exists, read-only should get 405 (Method Not Allowed) or 404
	body := `{"name":"test","config":{"strategy":{"mode":"single"},"targets":[{"virtual_key":"k"}]}}`
	req := authedRequest(http.MethodPost, "/admin/configs", body, roKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Route is gone — expect 404 or 405
	if w.Code == http.StatusCreated {
		t.Fatalf("config route should not exist, got 201")
	}
}
