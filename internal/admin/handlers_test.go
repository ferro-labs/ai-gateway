package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/go-chi/chi/v5"
)

type testConfigManager struct {
	cfg aigateway.Config
}

func (m *testConfigManager) GetConfig() aigateway.Config {
	return m.cfg
}

func (m *testConfigManager) ReloadConfig(cfg aigateway.Config) error {
	if err := aigateway.ValidateConfig(cfg); err != nil {
		return err
	}
	m.cfg = cfg
	return nil
}

func setupTestRouter() (*Handlers, chi.Router) {
	store := NewKeyStore()
	cm := &testConfigManager{
		cfg: aigateway.Config{
			Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
			Targets:  []aigateway.Target{{VirtualKey: "openai"}},
		},
	}
	h := &Handlers{
		Keys:    store,
		Configs: cm,
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

func TestGetConfig(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodGet, "/admin/config", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var cfg aigateway.Config
	if err := json.NewDecoder(w.Body).Decode(&cfg); err != nil {
		t.Fatalf("failed to decode config response: %v", err)
	}
	if cfg.Strategy.Mode != aigateway.ModeSingle {
		t.Fatalf("expected mode single, got %s", cfg.Strategy.Mode)
	}
}

func TestUpdateConfig(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	body := `{"strategy":{"mode":"fallback"},"targets":[{"virtual_key":"openai"},{"virtual_key":"anthropic"}]}`
	req := authedRequest(http.MethodPut, "/admin/config", body, adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	getReq := authedRequest(http.MethodGet, "/admin/config", "", adminKey)
	getW := httptest.NewRecorder()
	r.ServeHTTP(getW, getReq)

	var cfg aigateway.Config
	_ = json.NewDecoder(getW.Body).Decode(&cfg)
	if cfg.Strategy.Mode != aigateway.ModeFallback {
		t.Fatalf("expected updated mode fallback, got %s", cfg.Strategy.Mode)
	}
	if len(cfg.Targets) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(cfg.Targets))
	}
}

func TestUpdateConfigInvalidPayload(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	body := `{"strategy":{"mode":"invalid"},"targets":[]}`
	req := authedRequest(http.MethodPut, "/admin/config", body, adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestKeyUsageEndpoint(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)
	keyA, _ := h.Keys.Create("key-a", []string{ScopeReadOnly}, nil)
	keyB, _ := h.Keys.Create("key-b", []string{ScopeReadOnly}, nil)

	_, _ = h.Keys.ValidateKey(keyA.Key)
	_, _ = h.Keys.ValidateKey(keyA.Key)
	_, _ = h.Keys.ValidateKey(keyA.Key)
	_, _ = h.Keys.ValidateKey(keyB.Key)
	_, _ = h.Keys.ValidateKey(keyB.Key)

	req := authedRequest(http.MethodGet, "/admin/keys/usage?limit=2", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var payload struct {
		Data    []APIKey `json:"data"`
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
	activeKey, _ := h.Keys.Create("active-key", []string{ScopeReadOnly}, nil)
	inactiveKey, _ := h.Keys.Create("inactive-key", []string{ScopeReadOnly}, nil)
	_ = h.Keys.Revoke(inactiveKey.ID)
	_, _ = h.Keys.ValidateKey(activeKey.Key)

	req := authedRequest(http.MethodGet, "/admin/keys/usage?active=true", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var payload struct {
		Data []APIKey `json:"data"`
	}
	_ = json.NewDecoder(w.Body).Decode(&payload)
	for _, k := range payload.Data {
		if !k.Active {
			t.Fatalf("expected only active keys, got inactive key %s", k.Name)
		}
	}
}

func TestKeyUsageFilterSince(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)
	usedKey, _ := h.Keys.Create("used-key", []string{ScopeReadOnly}, nil)
	idleKey, _ := h.Keys.Create("idle-key", []string{ScopeReadOnly}, nil)
	_, _ = h.Keys.ValidateKey(usedKey.Key)

	since := time.Now().Add(-1 * time.Minute).UTC().Format(time.RFC3339)
	req := authedRequest(http.MethodGet, "/admin/keys/usage?since="+since, "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var payload struct {
		Data []APIKey `json:"data"`
	}
	_ = json.NewDecoder(w.Body).Decode(&payload)
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
	keyA, _ := h.Keys.Create("key-a", []string{ScopeReadOnly}, nil)
	keyB, _ := h.Keys.Create("key-b", []string{ScopeReadOnly}, nil)
	keyC, _ := h.Keys.Create("key-c", []string{ScopeReadOnly}, nil)

	_, _ = h.Keys.ValidateKey(keyA.Key)
	_, _ = h.Keys.ValidateKey(keyA.Key)
	time.Sleep(5 * time.Millisecond)
	_, _ = h.Keys.ValidateKey(keyB.Key)
	time.Sleep(5 * time.Millisecond)
	_, _ = h.Keys.ValidateKey(keyC.Key)

	req := httptest.NewRequest(http.MethodGet, "/admin/keys/usage?sort=usage&limit=4", nil)
	w := httptest.NewRecorder()
	h.keyUsage(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var usagePayload struct {
		Data []APIKey `json:"data"`
	}
	_ = json.NewDecoder(w.Body).Decode(&usagePayload)
	if len(usagePayload.Data) < 2 {
		t.Fatalf("expected at least 2 usage entries, got %d", len(usagePayload.Data))
	}
	for i := 1; i < len(usagePayload.Data); i++ {
		if usagePayload.Data[i-1].UsageCount < usagePayload.Data[i].UsageCount {
			t.Fatalf("usage sort should be descending, got %d then %d", usagePayload.Data[i-1].UsageCount, usagePayload.Data[i].UsageCount)
		}
	}

	secondExpected := usagePayload.Data[1].ID
	req = httptest.NewRequest(http.MethodGet, "/admin/keys/usage?sort=usage&limit=1&offset=1", nil)
	w = httptest.NewRecorder()
	h.keyUsage(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var offsetPayload struct {
		Data []APIKey `json:"data"`
	}
	_ = json.NewDecoder(w.Body).Decode(&offsetPayload)
	if len(offsetPayload.Data) != 1 {
		t.Fatalf("expected 1 result with limit=1, got %d", len(offsetPayload.Data))
	}
	if offsetPayload.Data[0].ID != secondExpected {
		t.Fatalf("offset pagination mismatch: expected id %s got %s", secondExpected, offsetPayload.Data[0].ID)
	}

	req = httptest.NewRequest(http.MethodGet, "/admin/keys/usage?sort=last_used&limit=4", nil)
	w = httptest.NewRecorder()
	h.keyUsage(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var recentPayload struct {
		Data []APIKey `json:"data"`
	}
	_ = json.NewDecoder(w.Body).Decode(&recentPayload)
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
