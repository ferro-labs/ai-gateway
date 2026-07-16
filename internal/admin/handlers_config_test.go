package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
)

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
	decodeJSON(t, getW.Body, &cfg)
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

func TestUpdateConfigPersistenceFailureReturnsServerError(t *testing.T) {
	cm := &persistenceFailingConfigManager{
		cfg: aigateway.Config{
			Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
			Targets:  []aigateway.Target{{VirtualKey: "openai"}},
		},
	}
	h, r := setupTestRouterWithConfigManager(cm)
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodPut, "/admin/config", fallbackConfigBody, adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
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
