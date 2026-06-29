//go:build integration
// +build integration

package integration

import (
	"context"
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/admin"
)

func TestPostgresConfigStore_SaveLoadRoundtrip(t *testing.T) {
	store, err := admin.NewPostgresConfigStore(testDSN)
	if err != nil {
		t.Fatalf("new config store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	cfg := aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeFallback},
		Targets: []aigateway.Target{
			{VirtualKey: "openai"},
			{VirtualKey: "anthropic"},
		},
	}
	if err := store.Save(context.Background(), cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, found, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !found {
		t.Fatal("expected config to be found")
	}
	if loaded.Strategy.Mode != aigateway.ModeFallback {
		t.Fatalf("expected fallback, got %q", loaded.Strategy.Mode)
	}
	if len(loaded.Targets) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(loaded.Targets))
	}
}

func TestPostgresConfigStore_Delete(t *testing.T) {
	store, err := admin.NewPostgresConfigStore(testDSN)
	if err != nil {
		t.Fatalf("new config store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	cfg := aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: "openai"}},
	}
	if err := store.Save(context.Background(), cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := store.Delete(context.Background()); err != nil {
		t.Fatalf("delete: %v", err)
	}

	_, found, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("load after delete: %v", err)
	}
	if found {
		t.Fatal("expected config not found after delete")
	}
}

func TestPostgresConfigManager_ReloadPersists(t *testing.T) {
	store, err := admin.NewPostgresConfigStore(testDSN)
	if err != nil {
		t.Fatalf("new config store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	initial := aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: "openai"}},
	}
	gw, err := aigateway.New(initial)
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}

	mgr, err := admin.NewGatewayConfigManager(gw, store)
	if err != nil {
		t.Fatalf("new config manager: %v", err)
	}

	next := aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeFallback},
		Targets:  []aigateway.Target{{VirtualKey: "openai"}, {VirtualKey: "anthropic"}},
	}
	if err := mgr.ReloadConfig(context.Background(), next); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if mgr.GetConfig().Strategy.Mode != aigateway.ModeFallback {
		t.Fatalf("expected fallback in manager, got %q", mgr.GetConfig().Strategy.Mode)
	}

	loaded, found, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("load from store: %v", err)
	}
	if !found {
		t.Fatal("expected persisted config")
	}
	if loaded.Strategy.Mode != aigateway.ModeFallback {
		t.Fatalf("expected persisted fallback, got %q", loaded.Strategy.Mode)
	}
}
