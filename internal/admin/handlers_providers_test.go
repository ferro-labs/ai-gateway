package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListProviders_NilRegistry(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	// h.Providers is nil by default in setupTestRouter.
	req := authedRequest(http.MethodGet, "/admin/providers", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var result []any
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty providers list, got %d", len(result))
	}
}
