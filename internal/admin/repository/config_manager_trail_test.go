package repository

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
)

// unappliableConfig passes ValidateConfig but names a plugin no factory is
// registered for, so the gateway refuses it at apply time. It is the shape that
// separates "valid on paper" from "the gateway can run this".
func unappliableConfig() config.Config {
	cfg := singleConfig()
	cfg.Plugins = []config.PluginConfig{{Name: "no-such-plugin", Type: "guardrail", Stage: "before_request", Enabled: true}}
	return cfg
}

// TestGatewayConfigManager_ReloadConfig_DoesNotPersistUnappliableConfig proves a
// config the gateway rejects never reaches the store. Persisting before the
// apply leaves it there for the next boot to adopt — a config the running
// gateway already refused.
func TestGatewayConfigManager_ReloadConfig_DoesNotPersistUnappliableConfig(t *testing.T) {
	gw, err := newTestGateway(t, singleConfig())
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	store := &failingConfigStore{}
	mgr, err := NewGatewayConfigManager(gw, store)
	if err != nil {
		t.Fatalf("new config manager: %v", err)
	}

	err = mgr.ReloadConfig(context.Background(), unappliableConfig())
	if err == nil {
		t.Fatal("expected the gateway to reject the config")
	}
	// The handler maps a persistence-classified error to 500 and everything else
	// to 400; a rejected config is the caller's fault and must stay a 400.
	if errors.Is(err, ErrConfigPersistence) {
		t.Fatalf("apply rejection classified as a persistence failure: %v", err)
	}
	if len(store.saved) != 0 {
		t.Fatalf("rejected config was persisted anyway: %+v", store.saved)
	}
	if got := mgr.GetConfig(); len(got.Plugins) != 0 {
		t.Fatalf("gateway config = %+v, want the pre-reload config unchanged", got)
	}
}

// TestGatewayConfigManager_ResetConfig_RecordsResetInHistory proves a reset
// leaves a trail entry of its own. Clearing the persisted override without
// recording it stops the trail's newest version from naming the running config,
// which is the divergence the trail exists to detect.
func TestGatewayConfigManager_ResetConfig_RecordsResetInHistory(t *testing.T) {
	initial := singleConfig()
	gw, err := newTestGateway(t, initial)
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	store, err := NewSQLiteConfigStore(t.Context(), filepath.Join(t.TempDir(), "config.db"))
	if err != nil {
		t.Fatalf("new config store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	mgr, err := NewGatewayConfigManager(gw, store)
	if err != nil {
		t.Fatalf("new config manager: %v", err)
	}
	if err := mgr.ReloadConfig(context.Background(), fallbackConfig()); err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if err := mgr.ResetConfig(context.Background()); err != nil {
		t.Fatalf("reset config: %v", err)
	}

	history, err := store.LoadHistory(context.Background())
	if err != nil {
		t.Fatalf("load history: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history len = %d, want 2 (the apply and the reset)", len(history))
	}
	newest := history[len(history)-1]
	if newest.Version != 2 {
		t.Fatalf("newest version = %d, want 2", newest.Version)
	}
	if newest.Config.Strategy.Mode != initial.Strategy.Mode {
		t.Fatalf("newest recorded mode = %q, want %q — the trail must name the running config",
			newest.Config.Strategy.Mode, initial.Strategy.Mode)
	}

	// The override itself is still gone: recording the reset must not resurrect
	// the persisted config a restart would load back.
	if _, ok, err := store.Load(context.Background()); err != nil || ok {
		t.Fatalf("persisted override still present after reset: ok=%v err=%v", ok, err)
	}
}
