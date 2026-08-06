package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/admin/model"
)

func TestKeyUsageEndpoint(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)
	keyA := createTestKey(t, h, "key-a", []string{model.ScopeReadOnly}, nil)
	keyB := createTestKey(t, h, "key-b", []string{model.ScopeReadOnly}, nil)

	validateTestKey(t, h, keyA.Key)
	validateTestKey(t, h, keyA.Key)
	validateTestKey(t, h, keyA.Key)
	validateTestKey(t, h, keyB.Key)
	validateTestKey(t, h, keyB.Key)

	req := authedRequest(http.MethodGet, "/admin/keys/usage?limit=2", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var payload struct {
		Data    []model.APIKey `json:"data"`
		Summary struct {
			TotalKeys    int   `json:"total_keys"`
			ActiveKeys   int   `json:"active_keys"`
			TotalUsage   int64 `json:"total_usage"`
			ReturnedKeys int   `json:"returned_keys"`
		} `json:"summary"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if payload.Summary.ReturnedKeys != 2 {
		t.Fatalf("expected returned_keys 2, got %d", payload.Summary.ReturnedKeys)
	}
	if len(payload.Data) != 2 {
		t.Fatalf("expected 2 keys in response data, got %d", len(payload.Data))
	}
	if payload.Data[0].Name != "key-a" {
		t.Fatalf("expected top key key-a, got %s", payload.Data[0].Name)
	}
	if payload.Data[0].UsageCount < payload.Data[1].UsageCount {
		t.Fatalf("expected descending usage sort, got %d then %d", payload.Data[0].UsageCount, payload.Data[1].UsageCount)
	}
}

func TestKeyUsageInvalidLimit(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodGet, "/admin/keys/usage?limit=bad", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestKeyUsageFilterActive(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)
	activeKey := createTestKey(t, h, "active-key", []string{model.ScopeReadOnly}, nil)
	inactiveKey := createTestKey(t, h, "inactive-key", []string{model.ScopeReadOnly}, nil)
	if err := h.Keys.Revoke(t.Context(), inactiveKey.ID); err != nil {
		t.Fatalf("revoke inactive test key: %v", err)
	}
	validateTestKey(t, h, activeKey.Key)

	req := authedRequest(http.MethodGet, "/admin/keys/usage?active=true", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var payload struct {
		Data []model.APIKey `json:"data"`
	}
	decodeJSON(t, w.Body, &payload)
	for _, k := range payload.Data {
		if !k.Active {
			t.Fatalf("expected only active keys, got inactive key %s", k.Name)
		}
	}
}

func TestKeyUsageFilterSince(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)
	usedKey := createTestKey(t, h, "used-key", []string{model.ScopeReadOnly}, nil)
	idleKey := createTestKey(t, h, "idle-key", []string{model.ScopeReadOnly}, nil)
	validateTestKey(t, h, usedKey.Key)

	since := time.Now().Add(-1 * time.Minute).UTC().Format(time.RFC3339)
	req := authedRequest(http.MethodGet, "/admin/keys/usage?since="+since, "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var payload struct {
		Data []model.APIKey `json:"data"`
	}
	decodeJSON(t, w.Body, &payload)
	if len(payload.Data) == 0 {
		t.Fatalf("expected at least one key")
	}
	for _, k := range payload.Data {
		if k.Name == idleKey.Name {
			t.Fatalf("did not expect key without recent usage in since-filtered results")
		}
	}
}

func TestKeyUsageInvalidFilters(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodGet, "/admin/keys/usage?active=nope", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid active filter, got %d", w.Code)
	}

	req = authedRequest(http.MethodGet, "/admin/keys/usage?since=badtime", "", adminKey)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid since filter, got %d", w.Code)
	}

	req = authedRequest(http.MethodGet, "/admin/keys/usage?offset=-1", "", adminKey)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid offset, got %d", w.Code)
	}

	req = authedRequest(http.MethodGet, "/admin/keys/usage?sort=unknown", "", adminKey)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid sort, got %d", w.Code)
	}
}

func TestKeyUsageOffsetAndSort(t *testing.T) {
	h, _ := setupTestRouter()
	keyA := createTestKey(t, h, "key-a", []string{model.ScopeReadOnly}, nil)
	keyB := createTestKey(t, h, "key-b", []string{model.ScopeReadOnly}, nil)
	keyC := createTestKey(t, h, "key-c", []string{model.ScopeReadOnly}, nil)

	// Validate in ascending recency order so each key ends up with a distinct,
	// non-nil LastUsedAt (C most recent, A oldest) and A carries the highest
	// usage count. The keyUsage handler sorts by these fields, so the
	// assertions below exercise the sort itself; the exact timestamp values are
	// immaterial as long as they are populated in this order.
	validateTestKey(t, h, keyA.Key)
	validateTestKey(t, h, keyA.Key)
	validateTestKey(t, h, keyB.Key)
	validateTestKey(t, h, keyC.Key)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/admin/keys/usage?sort=usage&limit=4", nil)
	w := httptest.NewRecorder()
	h.keyUsage(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var usagePayload struct {
		Data []model.APIKey `json:"data"`
	}
	decodeJSON(t, w.Body, &usagePayload)
	if len(usagePayload.Data) < 2 {
		t.Fatalf("expected at least 2 usage entries, got %d", len(usagePayload.Data))
	}
	for i := 1; i < len(usagePayload.Data); i++ {
		if usagePayload.Data[i-1].UsageCount < usagePayload.Data[i].UsageCount {
			t.Fatalf("usage sort should be descending, got %d then %d", usagePayload.Data[i-1].UsageCount, usagePayload.Data[i].UsageCount)
		}
	}

	secondExpected := usagePayload.Data[1].ID
	req = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/admin/keys/usage?sort=usage&limit=1&offset=1", nil)
	w = httptest.NewRecorder()
	h.keyUsage(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var offsetPayload struct {
		Data []model.APIKey `json:"data"`
	}
	decodeJSON(t, w.Body, &offsetPayload)
	if len(offsetPayload.Data) != 1 {
		t.Fatalf("expected 1 result with limit=1, got %d", len(offsetPayload.Data))
	}
	if offsetPayload.Data[0].ID != secondExpected {
		t.Fatalf("offset pagination mismatch: expected id %s got %s", secondExpected, offsetPayload.Data[0].ID)
	}

	req = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/admin/keys/usage?sort=last_used&limit=4", nil)
	w = httptest.NewRecorder()
	h.keyUsage(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var recentPayload struct {
		Data []model.APIKey `json:"data"`
	}
	decodeJSON(t, w.Body, &recentPayload)
	if len(recentPayload.Data) < 2 {
		t.Fatalf("expected at least 2 results for last_used sort")
	}
	for i := 1; i < len(recentPayload.Data); i++ {
		prev := recentPayload.Data[i-1].LastUsedAt
		curr := recentPayload.Data[i].LastUsedAt
		if prev == nil || curr == nil {
			continue
		}
		if prev.Before(*curr) {
			t.Fatalf("last_used sort should be descending")
		}
	}
}
