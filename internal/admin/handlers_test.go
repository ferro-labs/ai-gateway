package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/requestlog"
	"github.com/go-chi/chi/v5"
)

type testConfigManager struct {
	cfg     aigateway.Config
	initial aigateway.Config
}

const fallbackConfigBody = `{"strategy":{"mode":"fallback"},"targets":[{"virtual_key":"openai"},{"virtual_key":"anthropic"}]}`

type fakeLogReader struct {
	entries []requestlog.Entry
}

func (f *fakeLogReader) List(_ context.Context, query requestlog.Query) (requestlog.ListResult, error) {
	filtered := make([]requestlog.Entry, 0)
	for _, entry := range f.entries {
		if query.Stage != "" && entry.Stage != query.Stage {
			continue
		}
		if query.Model != "" && entry.Model != query.Model {
			continue
		}
		if query.Provider != "" && entry.Provider != query.Provider {
			continue
		}
		if query.Since != nil && entry.CreatedAt.Before(*query.Since) {
			continue
		}
		filtered = append(filtered, entry)
	}

	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].CreatedAt.After(filtered[j].CreatedAt)
	})

	start := query.Offset
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + query.Limit
	if query.Limit <= 0 || end > len(filtered) {
		end = len(filtered)
	}

	return requestlog.ListResult{Data: filtered[start:end], Total: len(filtered)}, nil
}

type fakeLogStore struct {
	entries []requestlog.Entry
}

func (f *fakeLogStore) List(_ context.Context, query requestlog.Query) (requestlog.ListResult, error) {
	reader := &fakeLogReader{entries: f.entries}
	return reader.List(context.Background(), query)
}

func (f *fakeLogStore) Delete(_ context.Context, query requestlog.MaintenanceQuery) (int, error) {
	if query.Before == nil {
		return 0, nil
	}

	remaining := make([]requestlog.Entry, 0, len(f.entries))
	deleted := 0
	for _, entry := range f.entries {
		if !entry.CreatedAt.Before(*query.Before) {
			remaining = append(remaining, entry)
			continue
		}
		if query.Stage != "" && entry.Stage != query.Stage {
			remaining = append(remaining, entry)
			continue
		}
		if query.Model != "" && entry.Model != query.Model {
			remaining = append(remaining, entry)
			continue
		}
		if query.Provider != "" && entry.Provider != query.Provider {
			remaining = append(remaining, entry)
			continue
		}
		deleted++
	}

	f.entries = remaining
	return deleted, nil
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

func (m *testConfigManager) ResetConfig() error {
	m.cfg = m.initial
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
	cm.initial = cm.cfg
	h := &Handlers{
		Keys:    store,
		Configs: cm,
	}
	r := chi.NewRouter()
	r.Use(AuthMiddleware(store))
	r.Mount("/admin", h.Routes())
	return h, r
}

func setupTestRouterWithLogs(reader requestlog.Reader) (*Handlers, chi.Router) {
	store := NewKeyStore()
	cm := &testConfigManager{
		cfg: aigateway.Config{
			Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
			Targets:  []aigateway.Target{{VirtualKey: "openai"}},
		},
	}
	cm.initial = cm.cfg
	h := &Handlers{
		Keys:    store,
		Configs: cm,
		Logs:    reader,
	}
	if maintainer, ok := reader.(requestlog.Maintainer); ok {
		h.LogAdmin = maintainer
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

func TestGetKeyByID(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)
	created, _ := h.Keys.Create("key-1", nil, nil)

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
	if got.Key == created.Key || len(got.Key) > 11 {
		t.Fatalf("expected masked key, got %q", got.Key)
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

func TestUpdateKeyExpiration(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)
	target, _ := h.Keys.Create("expirable", nil, nil)

	expiresAt := time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339)
	body := `{"expires_at":"` + expiresAt + `"}`
	req := authedRequest(http.MethodPut, "/admin/keys/"+target.ID, body, adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	fresh, ok := h.Keys.Get(target.ID)
	if !ok {
		t.Fatal("expected key to exist")
	}
	if fresh.ExpiresAt == nil {
		t.Fatal("expected expires_at to be set")
	}
}

func TestUpdateKeyClearExpiration(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)
	expiry := time.Now().Add(10 * time.Minute)
	target, _ := h.Keys.Create("expirable", nil, &expiry)

	body := `{"clear_expiration":true}`
	req := authedRequest(http.MethodPut, "/admin/keys/"+target.ID, body, adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	fresh, ok := h.Keys.Get(target.ID)
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
	target, _ := h.Keys.Create("expirable", nil, nil)

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

	body := fallbackConfigBody
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

func TestCreateConfig(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	body := `{"strategy":{"mode":"fallback"},"targets":[{"virtual_key":"openai"},{"virtual_key":"anthropic"}]}`
	req := authedRequest(http.MethodPost, "/admin/config", body, adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeleteConfig(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	updateBody := fallbackConfigBody
	updateReq := authedRequest(http.MethodPut, "/admin/config", updateBody, adminKey)
	updateW := httptest.NewRecorder()
	r.ServeHTTP(updateW, updateReq)
	if updateW.Code != http.StatusOK {
		t.Fatalf("expected update 200, got %d: %s", updateW.Code, updateW.Body.String())
	}

	deleteReq := authedRequest(http.MethodDelete, "/admin/config", "", adminKey)
	deleteW := httptest.NewRecorder()
	r.ServeHTTP(deleteW, deleteReq)
	if deleteW.Code != http.StatusOK {
		t.Fatalf("expected delete 200, got %d: %s", deleteW.Code, deleteW.Body.String())
	}

	getReq := authedRequest(http.MethodGet, "/admin/config", "", adminKey)
	getW := httptest.NewRecorder()
	r.ServeHTTP(getW, getReq)

	var cfg aigateway.Config
	if err := json.NewDecoder(getW.Body).Decode(&cfg); err != nil {
		t.Fatalf("decode config response: %v", err)
	}
	if cfg.Strategy.Mode != aigateway.ModeSingle {
		t.Fatalf("expected reset mode single, got %s", cfg.Strategy.Mode)
	}
}

func TestReadOnlyCannotDeleteConfig(t *testing.T) {
	h, r := setupTestRouter()
	createAdminKey(t, h)
	roKey := createReadOnlyKey(t, h)

	req := authedRequest(http.MethodDelete, "/admin/config", "", roKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
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

func TestGetConfigHistoryEmpty(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodGet, "/admin/config/history", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var payload struct {
		Data    []ConfigHistoryEntry `json:"data"`
		Summary struct {
			TotalVersions int `json:"total_versions"`
		} `json:"summary"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode history response: %v", err)
	}
	if payload.Summary.TotalVersions != 0 {
		t.Fatalf("expected total_versions 0, got %d", payload.Summary.TotalVersions)
	}
	if len(payload.Data) != 0 {
		t.Fatalf("expected empty history, got %d items", len(payload.Data))
	}
}

func TestConfigHistoryAfterUpdates(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	first := fallbackConfigBody
	req := authedRequest(http.MethodPut, "/admin/config", first, adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected first update 200, got %d: %s", w.Code, w.Body.String())
	}

	second := `{"strategy":{"mode":"single"},"targets":[{"virtual_key":"gemini"}]}`
	req = authedRequest(http.MethodPut, "/admin/config", second, adminKey)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected second update 200, got %d: %s", w.Code, w.Body.String())
	}

	historyReq := authedRequest(http.MethodGet, "/admin/config/history", "", adminKey)
	historyW := httptest.NewRecorder()
	r.ServeHTTP(historyW, historyReq)

	if historyW.Code != http.StatusOK {
		t.Fatalf("expected history 200, got %d: %s", historyW.Code, historyW.Body.String())
	}

	var payload struct {
		Data    []ConfigHistoryEntry `json:"data"`
		Summary struct {
			TotalVersions int `json:"total_versions"`
		} `json:"summary"`
	}
	if err := json.NewDecoder(historyW.Body).Decode(&payload); err != nil {
		t.Fatalf("decode history response: %v", err)
	}

	if payload.Summary.TotalVersions != 2 || len(payload.Data) != 2 {
		t.Fatalf("expected 2 history versions, summary=%d len=%d", payload.Summary.TotalVersions, len(payload.Data))
	}
	if payload.Data[0].Version != 1 || payload.Data[1].Version != 2 {
		t.Fatalf("unexpected history versions: %+v", payload.Data)
	}
	if payload.Data[0].Config.Strategy.Mode != aigateway.ModeFallback {
		t.Fatalf("expected first history mode fallback, got %s", payload.Data[0].Config.Strategy.Mode)
	}
	if payload.Data[1].Config.Strategy.Mode != aigateway.ModeSingle {
		t.Fatalf("expected second history mode single, got %s", payload.Data[1].Config.Strategy.Mode)
	}
}

func TestRollbackConfig(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	first := fallbackConfigBody
	req := authedRequest(http.MethodPut, "/admin/config", first, adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected first update 200, got %d: %s", w.Code, w.Body.String())
	}

	second := `{"strategy":{"mode":"single"},"targets":[{"virtual_key":"gemini"}]}`
	req = authedRequest(http.MethodPut, "/admin/config", second, adminKey)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected second update 200, got %d: %s", w.Code, w.Body.String())
	}

	req = authedRequest(http.MethodPost, "/admin/config/rollback/1", "", adminKey)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected rollback 200, got %d: %s", w.Code, w.Body.String())
	}

	getReq := authedRequest(http.MethodGet, "/admin/config", "", adminKey)
	getW := httptest.NewRecorder()
	r.ServeHTTP(getW, getReq)

	var cfg aigateway.Config
	if err := json.NewDecoder(getW.Body).Decode(&cfg); err != nil {
		t.Fatalf("decode config response: %v", err)
	}
	if cfg.Strategy.Mode != aigateway.ModeFallback {
		t.Fatalf("expected rolled back mode fallback, got %s", cfg.Strategy.Mode)
	}
	if len(cfg.Targets) != 2 {
		t.Fatalf("expected rolled back config with 2 targets, got %d", len(cfg.Targets))
	}

	historyReq := authedRequest(http.MethodGet, "/admin/config/history", "", adminKey)
	historyW := httptest.NewRecorder()
	r.ServeHTTP(historyW, historyReq)

	var historyPayload struct {
		Data []ConfigHistoryEntry `json:"data"`
	}
	if err := json.NewDecoder(historyW.Body).Decode(&historyPayload); err != nil {
		t.Fatalf("decode history response: %v", err)
	}
	if len(historyPayload.Data) != 3 {
		t.Fatalf("expected 3 history entries after rollback, got %d", len(historyPayload.Data))
	}
	last := historyPayload.Data[2]
	if last.RolledBackFrom == nil || *last.RolledBackFrom != 2 {
		t.Fatalf("expected rolled_back_from=2, got %+v", last.RolledBackFrom)
	}
}

func TestRollbackConfigInvalidVersion(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodPost, "/admin/config/rollback/not-a-number", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRollbackConfigVersionNotFound(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodPost, "/admin/config/rollback/1", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestReadOnlyCannotRollbackConfig(t *testing.T) {
	h, r := setupTestRouter()
	createAdminKey(t, h)
	roKey := createReadOnlyKey(t, h)

	req := authedRequest(http.MethodPost, "/admin/config/rollback/1", "", roKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
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

func TestLogsEndpoint(t *testing.T) {
	now := time.Now().UTC()
	reader := &fakeLogReader{entries: []requestlog.Entry{
		{TraceID: "1", Stage: "after_request", Model: "gpt-4", Provider: "openai", TotalTokens: 10, CreatedAt: now.Add(-2 * time.Minute)},
		{TraceID: "2", Stage: "on_error", Model: "gpt-4", Provider: "openai", ErrorMessage: "boom", CreatedAt: now.Add(-1 * time.Minute)},
	}}
	h, r := setupTestRouterWithLogs(reader)
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodGet, "/admin/logs?stage=on_error&limit=10", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var payload struct {
		Data    []requestlog.Entry `json:"data"`
		Summary struct {
			TotalEntries    int `json:"total_entries"`
			ReturnedEntries int `json:"returned_entries"`
		} `json:"summary"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode logs response: %v", err)
	}
	if payload.Summary.TotalEntries != 1 || payload.Summary.ReturnedEntries != 1 {
		t.Fatalf("unexpected summary: %+v", payload.Summary)
	}
	if len(payload.Data) != 1 || payload.Data[0].Stage != "on_error" {
		t.Fatalf("expected filtered on_error entry")
	}
}

func TestDashboardEndpoint(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeLogStore{entries: []requestlog.Entry{
		{TraceID: "1", Stage: "after_request", Provider: "openai", CreatedAt: now.Add(-2 * time.Minute)},
		{TraceID: "2", Stage: "on_error", Provider: "openai", CreatedAt: now.Add(-1 * time.Minute)},
	}}
	h, r := setupTestRouterWithLogs(store)
	adminKey := createAdminKey(t, h)

	expiredAt := now.Add(-10 * time.Minute)
	_, _ = h.Keys.Create("expired-key", []string{ScopeReadOnly}, &expiredAt)
	active, _ := h.Keys.Create("active-key", []string{ScopeReadOnly}, nil)
	_, _ = h.Keys.ValidateKey(active.Key)

	req := authedRequest(http.MethodGet, "/admin/dashboard", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var payload struct {
		Providers struct {
			Total int `json:"total"`
		} `json:"providers"`
		Keys struct {
			Total      int   `json:"total"`
			Active     int   `json:"active"`
			Expired    int   `json:"expired"`
			TotalUsage int64 `json:"total_usage"`
		} `json:"keys"`
		RequestLogs struct {
			Enabled bool `json:"enabled"`
			Total   int  `json:"total"`
		} `json:"request_logs"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode dashboard payload: %v", err)
	}

	if payload.Providers.Total < 0 {
		t.Fatalf("invalid providers total: %d", payload.Providers.Total)
	}
	if payload.Keys.Total < 3 {
		t.Fatalf("expected at least 3 keys, got %d", payload.Keys.Total)
	}
	if payload.Keys.Expired < 1 {
		t.Fatalf("expected at least one expired key, got %d", payload.Keys.Expired)
	}
	if payload.Keys.TotalUsage < 1 {
		t.Fatalf("expected usage to be recorded, got %d", payload.Keys.TotalUsage)
	}
	if !payload.RequestLogs.Enabled {
		t.Fatal("expected request logs to be enabled")
	}
	if payload.RequestLogs.Total != 2 {
		t.Fatalf("expected request log total 2, got %d", payload.RequestLogs.Total)
	}
}

func TestDashboardEndpointWithoutLogs(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodGet, "/admin/dashboard", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var payload struct {
		RequestLogs struct {
			Enabled bool `json:"enabled"`
			Total   int  `json:"total"`
		} `json:"request_logs"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode dashboard payload: %v", err)
	}
	if payload.RequestLogs.Enabled {
		t.Fatal("expected request logs to be disabled")
	}
	if payload.RequestLogs.Total != 0 {
		t.Fatalf("expected request logs total 0, got %d", payload.RequestLogs.Total)
	}
}

func TestLogsEndpointNotEnabled(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodGet, "/admin/logs", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", w.Code)
	}
}

func TestLogsEndpointInvalidSince(t *testing.T) {
	reader := &fakeLogReader{entries: []requestlog.Entry{}}
	h, r := setupTestRouterWithLogs(reader)
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodGet, "/admin/logs?since=bad", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestLogsStatsEndpoint(t *testing.T) {
	now := time.Now().UTC()
	reader := &fakeLogReader{entries: []requestlog.Entry{
		{TraceID: "1", Stage: "after_request", Model: "gpt-4", Provider: "openai", TotalTokens: 10, CreatedAt: now.Add(-3 * time.Minute)},
		{TraceID: "2", Stage: "on_error", Model: "gpt-4", Provider: "openai", ErrorMessage: "boom", TotalTokens: 20, CreatedAt: now.Add(-2 * time.Minute)},
		{TraceID: "3", Stage: "after_request", Model: "claude", Provider: "anthropic", TotalTokens: 5, CreatedAt: now.Add(-1 * time.Minute)},
	}}
	h, r := setupTestRouterWithLogs(reader)
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodGet, "/admin/logs/stats", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var payload struct {
		Summary struct {
			TotalEntries int `json:"total_entries"`
			ErrorEntries int `json:"error_entries"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"summary"`
		ByStage    map[string]int `json:"by_stage"`
		ByProvider map[string]int `json:"by_provider"`
		ByModel    map[string]int `json:"by_model"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode logs stats response: %v", err)
	}
	if payload.Summary.TotalEntries != 3 {
		t.Fatalf("expected total_entries=3, got %d", payload.Summary.TotalEntries)
	}
	if payload.Summary.ErrorEntries != 1 {
		t.Fatalf("expected error_entries=1, got %d", payload.Summary.ErrorEntries)
	}
	if payload.Summary.TotalTokens != 35 {
		t.Fatalf("expected total_tokens=35, got %d", payload.Summary.TotalTokens)
	}
	if payload.ByStage["after_request"] != 2 || payload.ByStage["on_error"] != 1 {
		t.Fatalf("unexpected by_stage: %+v", payload.ByStage)
	}
	if payload.ByProvider["openai"] != 2 || payload.ByProvider["anthropic"] != 1 {
		t.Fatalf("unexpected by_provider: %+v", payload.ByProvider)
	}
	if payload.ByModel["gpt-4"] != 2 || payload.ByModel["claude"] != 1 {
		t.Fatalf("unexpected by_model: %+v", payload.ByModel)
	}
}

func TestLogsStatsEndpointWithLimit(t *testing.T) {
	now := time.Now().UTC()
	reader := &fakeLogReader{entries: []requestlog.Entry{
		{TraceID: "1", Stage: "after_request", Model: "gpt-4", Provider: "openai", TotalTokens: 10, CreatedAt: now.Add(-3 * time.Minute)},
		{TraceID: "2", Stage: "on_error", Model: "gpt-4", Provider: "openai", ErrorMessage: "boom", TotalTokens: 20, CreatedAt: now.Add(-2 * time.Minute)},
		{TraceID: "3", Stage: "after_request", Model: "claude", Provider: "anthropic", TotalTokens: 5, CreatedAt: now.Add(-1 * time.Minute)},
	}}
	h, r := setupTestRouterWithLogs(reader)
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodGet, "/admin/logs/stats?limit=1", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var payload struct {
		Summary struct {
			TotalEntries int `json:"total_entries"`
		} `json:"summary"`
		ByProvider map[string]int `json:"by_provider"`
		ByModel    map[string]int `json:"by_model"`
		Filters    struct {
			Limit int `json:"limit"`
		} `json:"filters"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode logs stats response: %v", err)
	}
	if payload.Summary.TotalEntries != 3 {
		t.Fatalf("expected total_entries=3, got %d", payload.Summary.TotalEntries)
	}
	if payload.Filters.Limit != 1 {
		t.Fatalf("expected filters.limit=1, got %d", payload.Filters.Limit)
	}
	if len(payload.ByProvider) != 1 || payload.ByProvider["openai"] != 2 {
		t.Fatalf("unexpected limited by_provider: %+v", payload.ByProvider)
	}
	if len(payload.ByModel) != 1 || payload.ByModel["gpt-4"] != 2 {
		t.Fatalf("unexpected limited by_model: %+v", payload.ByModel)
	}
}

func TestLogsStatsEndpointTruncatesLargeDatasets(t *testing.T) {
	now := time.Now().UTC()
	entries := make([]requestlog.Entry, 0, logsStatsMaxScannedEntries+10)
	for i := 0; i < logsStatsMaxScannedEntries+10; i++ {
		entries = append(entries, requestlog.Entry{
			TraceID:      "trace",
			Stage:        "after_request",
			Model:        "gpt-4",
			Provider:     "openai",
			TotalTokens:  1,
			ErrorMessage: "",
			CreatedAt:    now.Add(-time.Duration(i) * time.Second),
		})
	}

	reader := &fakeLogReader{entries: entries}
	h, r := setupTestRouterWithLogs(reader)
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodGet, "/admin/logs/stats", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var payload struct {
		Summary struct {
			TotalEntries     int  `json:"total_entries"`
			AvailableEntries int  `json:"available_entries"`
			Truncated        bool `json:"truncated"`
			ScanLimit        int  `json:"scan_limit"`
		} `json:"summary"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode logs stats response: %v", err)
	}

	if !payload.Summary.Truncated {
		t.Fatal("expected truncated=true for oversized dataset")
	}
	if payload.Summary.TotalEntries != logsStatsMaxScannedEntries {
		t.Fatalf("expected total_entries=%d, got %d", logsStatsMaxScannedEntries, payload.Summary.TotalEntries)
	}
	if payload.Summary.AvailableEntries != logsStatsMaxScannedEntries+10 {
		t.Fatalf("expected available_entries=%d, got %d", logsStatsMaxScannedEntries+10, payload.Summary.AvailableEntries)
	}
	if payload.Summary.ScanLimit != logsStatsMaxScannedEntries {
		t.Fatalf("expected scan_limit=%d, got %d", logsStatsMaxScannedEntries, payload.Summary.ScanLimit)
	}
}

func TestLogsStatsEndpointInvalidLimit(t *testing.T) {
	reader := &fakeLogReader{entries: []requestlog.Entry{}}
	h, r := setupTestRouterWithLogs(reader)
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodGet, "/admin/logs/stats?limit=bad", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestLogsStatsEndpointInvalidSince(t *testing.T) {
	reader := &fakeLogReader{entries: []requestlog.Entry{}}
	h, r := setupTestRouterWithLogs(reader)
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodGet, "/admin/logs/stats?since=bad", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestLogsStatsEndpointNotEnabled(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodGet, "/admin/logs/stats", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", w.Code)
	}
}

func TestDeleteLogsEndpoint(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeLogStore{entries: []requestlog.Entry{
		{TraceID: "1", Stage: "on_error", Provider: "openai", CreatedAt: now.Add(-2 * time.Hour)},
		{TraceID: "2", Stage: "after_request", Provider: "openai", CreatedAt: now.Add(-90 * time.Minute)},
		{TraceID: "3", Stage: "on_error", Provider: "openai", CreatedAt: now.Add(-10 * time.Minute)},
	}}
	h, r := setupTestRouterWithLogs(store)
	adminKey := createAdminKey(t, h)

	before := now.Add(-30 * time.Minute).Format(time.RFC3339)
	req := authedRequest(http.MethodDelete, "/admin/logs?before="+before+"&stage=on_error", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var payload struct {
		Deleted int `json:"deleted"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode delete logs response: %v", err)
	}
	if payload.Deleted != 1 {
		t.Fatalf("expected deleted=1, got %d", payload.Deleted)
	}

	listReq := authedRequest(http.MethodGet, "/admin/logs?stage=on_error", "", adminKey)
	listW := httptest.NewRecorder()
	r.ServeHTTP(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("expected list 200, got %d: %s", listW.Code, listW.Body.String())
	}

	var listPayload struct {
		Summary struct {
			TotalEntries int `json:"total_entries"`
		} `json:"summary"`
	}
	if err := json.NewDecoder(listW.Body).Decode(&listPayload); err != nil {
		t.Fatalf("decode list logs response: %v", err)
	}
	if listPayload.Summary.TotalEntries != 1 {
		t.Fatalf("expected one on_error entry after cleanup, got %d", listPayload.Summary.TotalEntries)
	}
}

func TestDeleteLogsEndpointMissingBefore(t *testing.T) {
	store := &fakeLogStore{entries: []requestlog.Entry{}}
	h, r := setupTestRouterWithLogs(store)
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodDelete, "/admin/logs", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestDeleteLogsEndpointInvalidBefore(t *testing.T) {
	store := &fakeLogStore{entries: []requestlog.Entry{}}
	h, r := setupTestRouterWithLogs(store)
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodDelete, "/admin/logs?before=bad", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestDeleteLogsEndpointNotEnabled(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodDelete, "/admin/logs?before=2026-02-01T00:00:00Z", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", w.Code)
	}
}
