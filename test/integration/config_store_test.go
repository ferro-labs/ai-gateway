//go:build integration
// +build integration

package integration

import (
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/internal/admin/repository"
)

func TestPostgresConfigStore_SaveLoadRoundtrip(t *testing.T) {
	store, err := repository.NewPostgresConfigStore(t.Context(), testDSN)
	if err != nil {
		t.Fatalf("new config store: %v", err)
	}
	resetTablesAndClose(t, store, "config_history", "gateway_config")

	cfg := config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets: []config.Target{
			{VirtualKey: "openai"},
			{VirtualKey: "anthropic"},
		},
	}
	if err := store.Save(t.Context(), cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, found, err := store.Load(t.Context())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !found {
		t.Fatal("expected config to be found")
	}
	if loaded.Strategy.Mode != config.ModeFallback {
		t.Fatalf("expected fallback, got %q", loaded.Strategy.Mode)
	}
	if len(loaded.Targets) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(loaded.Targets))
	}
}

func TestPostgresConfigStore_Delete(t *testing.T) {
	store, err := repository.NewPostgresConfigStore(t.Context(), testDSN)
	if err != nil {
		t.Fatalf("new config store: %v", err)
	}
	resetTablesAndClose(t, store, "config_history", "gateway_config")

	cfg := config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "openai"}},
	}
	if err := store.Save(t.Context(), cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := store.Delete(t.Context()); err != nil {
		t.Fatalf("delete: %v", err)
	}

	_, found, err := store.Load(t.Context())
	if err != nil {
		t.Fatalf("load after delete: %v", err)
	}
	if found {
		t.Fatal("expected config not found after delete")
	}
}

func TestPostgresConfigManager_ReloadPersists(t *testing.T) {
	store, err := repository.NewPostgresConfigStore(t.Context(), testDSN)
	if err != nil {
		t.Fatalf("new config store: %v", err)
	}
	resetTablesAndClose(t, store, "config_history", "gateway_config")

	initial := config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "openai"}},
	}
	gw, err := newTestGateway(t, initial)
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}

	mgr, err := repository.NewGatewayConfigManager(gw, store)
	if err != nil {
		t.Fatalf("new config manager: %v", err)
	}

	next := config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets:  []config.Target{{VirtualKey: "openai"}, {VirtualKey: "anthropic"}},
	}
	if err := mgr.ReloadConfig(t.Context(), next); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if mgr.GetConfig().Strategy.Mode != config.ModeFallback {
		t.Fatalf("expected fallback in manager, got %q", mgr.GetConfig().Strategy.Mode)
	}

	loaded, found, err := store.Load(t.Context())
	if err != nil {
		t.Fatalf("load from store: %v", err)
	}
	if !found {
		t.Fatal("expected persisted config")
	}
	if loaded.Strategy.Mode != config.ModeFallback {
		t.Fatalf("expected persisted fallback, got %q", loaded.Strategy.Mode)
	}
}
