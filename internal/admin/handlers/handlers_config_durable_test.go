package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/internal/admin/model"
	"github.com/ferro-labs/ai-gateway/internal/admin/repository"
	"github.com/go-chi/chi/v5"
)

// durableConfigManager stands in for a SQL-backed config manager: its version
// trail is seeded with versions applied before this process started, exactly
// what a restart leaves behind, and every apply appends to that same trail
// rather than to a counter the handler keeps.
type durableConfigManager struct {
	testConfigManager
	history []model.PersistedConfigVersion
}

func (m *durableConfigManager) ReloadConfig(ctx context.Context, cfg config.Config) error {
	if err := m.testConfigManager.ReloadConfig(ctx, cfg); err != nil {
		return err
	}
	next := 1
	if n := len(m.history); n > 0 {
		next = m.history[n-1].Version + 1
	}
	m.history = append(m.history, model.PersistedConfigVersion{
		Version:   next,
		Config:    cfg,
		UpdatedAt: time.Now().UTC(),
		Actor:     model.ActorFromContext(ctx),
	})
	return nil
}

func (m *durableConfigManager) LoadHistory(context.Context) ([]model.PersistedConfigVersion, bool, error) {
	return m.history, true, nil
}

var _ repository.ConfigHistoryLoader = (*durableConfigManager)(nil)

// setupTestRouterWithDurableHistory wires a manager whose trail already holds
// version 7 — applied before this process booted — with the handler's own
// in-memory history empty, the state every restart produces.
func setupTestRouterWithDurableHistory() (*Handlers, chi.Router, *durableConfigManager) {
	cm := &durableConfigManager{}
	cm.cfg = config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "openai"}},
	}
	cm.initial = cm.cfg
	cm.history = []model.PersistedConfigVersion{{
		Version: 7,
		Config: config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeFallback},
			Targets:  []config.Target{{VirtualKey: "openai"}, {VirtualKey: "anthropic"}},
		},
		UpdatedAt: time.Now().UTC().Add(-time.Hour),
		Actor:     "key:earlier",
	}}

	store := repository.NewKeyStore()
	h := &Handlers{Keys: store, Configs: cm}
	r := chi.NewRouter()
	r.Use(AuthMiddleware(store, ""))
	r.Mount("/admin", h.Routes())
	return h, r, cm
}

func getConfigHistoryEntries(t *testing.T, r chi.Router, key *model.APIKey) []ConfigHistoryEntry {
	t.Helper()
	req := authedRequest(http.MethodGet, "/admin/config/history", "", key)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected history 200, got %d: %s", w.Code, w.Body.String())
	}
	var payload struct {
		Data []ConfigHistoryEntry `json:"data"`
	}
	decodeJSON(t, w.Body, &payload)
	return payload.Data
}

// TestConfigHistoryServesDurableTrail proves the versions a restart persisted
// are the ones served. Serving the in-memory list instead reports an empty
// history for a gateway that has applied seven configs.
func TestConfigHistoryServesDurableTrail(t *testing.T) {
	h, r, _ := setupTestRouterWithDurableHistory()
	adminKey := createAdminKey(t, h)

	entries := getConfigHistoryEntries(t, r, adminKey)
	if len(entries) != 1 {
		t.Fatalf("history len = %d, want 1", len(entries))
	}
	if entries[0].Version != 7 {
		t.Fatalf("version = %d, want 7", entries[0].Version)
	}
	if entries[0].Actor != "key:earlier" {
		t.Fatalf("actor = %q, want %q", entries[0].Actor, "key:earlier")
	}
}

// TestConfigHistoryNumbersFromOneCounter proves an apply continues the durable
// trail's numbering. With the handler numbering separately, the version served
// as 1 and the version the store recorded as 8 are the same config under two
// numbers, and a rollback to either one means something different.
func TestConfigHistoryNumbersFromOneCounter(t *testing.T) {
	h, r, cm := setupTestRouterWithDurableHistory()
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodPut, "/admin/config", fallbackConfigBody, adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected update 200, got %d: %s", w.Code, w.Body.String())
	}

	entries := getConfigHistoryEntries(t, r, adminKey)
	if len(entries) != 2 {
		t.Fatalf("history len = %d, want 2", len(entries))
	}
	if entries[1].Version != 8 {
		t.Fatalf("newest served version = %d, want 8", entries[1].Version)
	}
	if got := cm.history[len(cm.history)-1].Version; got != entries[1].Version {
		t.Fatalf("stored version %d and served version %d name the same config", got, entries[1].Version)
	}
}

// TestRollbackTargetsDurableVersion proves a version applied before the restart
// can still be rolled back to. Against the in-memory list the same request is a
// 404 for a version the history endpoint just listed.
func TestRollbackTargetsDurableVersion(t *testing.T) {
	h, r, _ := setupTestRouterWithDurableHistory()
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodPost, "/admin/config/rollback/7", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected rollback 200, got %d: %s", w.Code, w.Body.String())
	}

	getReq := authedRequest(http.MethodGet, "/admin/config", "", adminKey)
	getW := httptest.NewRecorder()
	r.ServeHTTP(getW, getReq)
	var cfg config.Config
	decodeJSON(t, getW.Body, &cfg)
	if cfg.Strategy.Mode != config.ModeFallback {
		t.Fatalf("active mode = %q, want %q", cfg.Strategy.Mode, config.ModeFallback)
	}

	entries := getConfigHistoryEntries(t, r, adminKey)
	if len(entries) != 2 || entries[1].Version != 8 {
		t.Fatalf("history after rollback = %+v, want a version 8 appended", entries)
	}
}
