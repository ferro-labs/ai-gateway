package handlers

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/internal/admin/repository"
	"github.com/go-chi/chi/v5"
)

// The rollback provenance the History tab renders was memory-only: on any
// deployment with a config store — which is every production one — the
// "Rolled back from" column showed "—" for a real rollback, making it
// indistinguishable from an ordinary version bump.
//
// This drives the whole path a browser drives: PUT, PUT, POST rollback, GET
// history, all against a real SQL-backed manager rather than a stub that could
// agree with the handler while the store did not.
func TestRollbackConfig_DurableHistoryNamesTheVersionItReplaced(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "openai"}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	store, err := repository.NewSQLiteConfigStore(t.Context(), filepath.Join(t.TempDir(), "config.db"))
	if err != nil {
		t.Fatalf("new config store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := repository.NewGatewayConfigManager(gw, store)
	if err != nil {
		t.Fatalf("new config manager: %v", err)
	}

	keys := repository.NewKeyStore()
	h := &Handlers{Keys: keys, Configs: manager}
	r := chi.NewRouter()
	r.Use(AuthMiddleware(keys, ""))
	r.Mount("/admin", h.Routes())
	adminKey := createAdminKey(t, h)

	put := func(body string) {
		t.Helper()
		w := httptest.NewRecorder()
		r.ServeHTTP(w, authedRequest(http.MethodPut, "/admin/config", body, adminKey))
		if w.Code != http.StatusOK {
			t.Fatalf("PUT /admin/config = %d: %s", w.Code, w.Body.String())
		}
	}
	put(`{"strategy":{"mode":"single"},"targets":[{"virtual_key":"openai"}]}`)
	put(`{"strategy":{"mode":"fallback"},"targets":[{"virtual_key":"openai"},{"virtual_key":"anthropic"}]}`)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodPost, "/admin/config/rollback/1", "", adminKey))
	if w.Code != http.StatusOK {
		t.Fatalf("POST rollback = %d: %s", w.Code, w.Body.String())
	}

	historyW := httptest.NewRecorder()
	r.ServeHTTP(historyW, authedRequest(http.MethodGet, "/admin/config/history", "", adminKey))
	if historyW.Code != http.StatusOK {
		t.Fatalf("GET history = %d: %s", historyW.Code, historyW.Body.String())
	}
	var payload struct {
		Data []ConfigHistoryEntry `json:"data"`
	}
	decodeJSON(t, historyW.Body, &payload)

	if len(payload.Data) != 3 {
		t.Fatalf("history has %d versions, want 3", len(payload.Data))
	}
	for _, entry := range payload.Data[:2] {
		if entry.RolledBackFrom != nil {
			t.Errorf("version %d reports rolled_back_from = %d; it was an ordinary update",
				entry.Version, *entry.RolledBackFrom)
		}
	}
	rollback := payload.Data[2]
	if rollback.RolledBackFrom == nil {
		t.Fatal("the durable history reports the rollback as an ordinary update, so the dashboard column reads \"—\" for it")
	}
	if got := *rollback.RolledBackFrom; got != 2 {
		t.Errorf("rolled_back_from = %d, want 2", got)
	}
	// The restored config is what makes it a rollback rather than a relabelled
	// update.
	if rollback.Config.Strategy.Mode != config.ModeSingle {
		t.Errorf("rolled-back version carries mode %q, want %q", rollback.Config.Strategy.Mode, config.ModeSingle)
	}
}
