package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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
	decodeJSON(t, w.Body, &created)
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
	decodeJSON(t, w.Body, &created)
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
	createTestKey(t, h, "key-1", nil, nil)
	createTestKey(t, h, "key-2", nil, nil)

	req := authedRequest(http.MethodGet, "/admin/keys", "", key)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var keys []*APIKey
	decodeJSON(t, w.Body, &keys)
	if len(keys) != 3 { // admin key + 2 created
		t.Fatalf("expected 3 keys, got %d", len(keys))
	}
	for _, k := range keys {
		if !strings.Contains(k.Key, "...") {
			t.Errorf("expected a display key, got %s", k.Key)
		}
	}
}

func TestGetKeyByID(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)
	created := createTestKey(t, h, "key-1", nil, nil)

	req := authedRequest(http.MethodGet, "/admin/keys/"+created.ID, "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var got APIKey
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode key response: %v", err)
	}
	if got.ID != created.ID {
		t.Fatalf("expected id %s, got %s", created.ID, got.ID)
	}
	if got.Key == created.Key {
		t.Fatalf("the full secret was returned by GET /admin/keys/{id}: %q", got.Key)
	}
	if got.Key != displayKey(created.Key) {
		t.Fatalf("expected the display key %q, got %q", displayKey(created.Key), got.Key)
	}
}

func TestGetKeyByIDNotFound(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodGet, "/admin/keys/not-found", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestUpdateKey(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)
	target := createTestKey(t, h, "original", nil, nil)

	body := `{"name":"updated","scopes":["read_only"]}`
	req := authedRequest(http.MethodPut, "/admin/keys/"+target.ID, body, adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var updated APIKey
	decodeJSON(t, w.Body, &updated)
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

func TestUpdateKeyExpiration(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)
	target := createTestKey(t, h, "expirable", nil, nil)

	expectedExpiry := time.Now().Add(10 * time.Minute).UTC().Truncate(time.Second)
	body := `{"expires_at":"` + expectedExpiry.Format(time.RFC3339) + `"}`
	req := authedRequest(http.MethodPut, "/admin/keys/"+target.ID, body, adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	fresh, ok := h.Keys.Get(context.Background(), target.ID)
	if !ok {
		t.Fatal("expected key to exist")
	}
	if fresh.ExpiresAt == nil {
		t.Fatal("expected expires_at to be set")
	}
	if !fresh.ExpiresAt.Equal(expectedExpiry) {
		t.Fatalf("expires_at = %s, want %s", fresh.ExpiresAt.UTC(), expectedExpiry)
	}
}

func TestUpdateKeyClearExpiration(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)
	expiry := time.Now().Add(10 * time.Minute)
	target := createTestKey(t, h, "expirable", nil, &expiry)

	body := `{"clear_expiration":true}`
	req := authedRequest(http.MethodPut, "/admin/keys/"+target.ID, body, adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	fresh, ok := h.Keys.Get(context.Background(), target.ID)
	if !ok {
		t.Fatal("expected key to exist")
	}
	if fresh.ExpiresAt != nil {
		t.Fatal("expected expires_at to be cleared")
	}
}

func TestUpdateKeyInvalidExpiration(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)
	target := createTestKey(t, h, "expirable", nil, nil)

	body := `{"expires_at":"not-a-timestamp"}`
	req := authedRequest(http.MethodPut, "/admin/keys/"+target.ID, body, adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeleteKey(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)
	target := createTestKey(t, h, "to-delete", nil, nil)

	req := authedRequest(http.MethodDelete, "/admin/keys/"+target.ID, "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	if _, ok := h.Keys.Get(context.Background(), target.ID); ok {
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
	target := createTestKey(t, h, "to-revoke", nil, nil)

	req := authedRequest(http.MethodPost, "/admin/keys/"+target.ID+"/revoke", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	k, ok := h.Keys.Get(context.Background(), target.ID)
	if !ok {
		t.Fatal("expected key to exist")
	}
	if k.Active {
		t.Error("expected key to be inactive")
	}
}
