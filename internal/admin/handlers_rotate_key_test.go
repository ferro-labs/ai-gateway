package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRotateKey(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	// Create a key to rotate, save its original key string before rotation mutates it.
	key, err := h.Keys.Create(context.Background(), "rotatable-key", []string{ScopeReadOnly}, nil)
	if err != nil {
		t.Fatalf("failed to create key: %v", err)
	}
	keyID := key.ID

	req := authedRequest(http.MethodPost, "/admin/keys/"+keyID+"/rotate", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var rotated APIKey
	if err := json.NewDecoder(w.Body).Decode(&rotated); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if rotated.ID != keyID {
		t.Errorf("expected rotated key to keep ID %q, got %q", keyID, rotated.ID)
	}
	if !strings.HasPrefix(rotated.Key, "fgw_") {
		t.Errorf("expected rotated key to start with fgw_, got %q", rotated.Key)
	}
}

func TestRotateKeyNotFound(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodPost, "/admin/keys/nonexistent/rotate", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}
