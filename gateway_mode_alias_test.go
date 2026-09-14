package aigateway

import (
	"context"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
)

// ValidateConfig takes the config by value and normalises only its own copy,
// so New and ReloadConfig installed the legacy "loadbalance" spelling as-is.
// Validation passed, and the first routed request then failed in the strategy
// factory with "unknown strategy mode: loadbalance" — the upgrade path for
// every persisted pre-v1.5.6 load-balancing config, and for every managed
// tenant whose config the control plane hands to New.
func legacyLoadBalanceConfig() config.Config {
	return config.Config{
		Strategy: config.StrategyConfig{Mode: "loadbalance"},
		Targets: []config.Target{
			{VirtualKey: "openai", Weight: 3},
			{VirtualKey: "anthropic", Weight: 1},
		},
	}
}

func TestNew_NormalizesLegacyLoadBalanceMode(t *testing.T) {
	gw, err := New(legacyLoadBalanceConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = gw.Close() })

	if got := gw.config.Strategy.Mode; got != config.ModeLoadBalance {
		t.Fatalf("installed mode = %q, want %q", got, config.ModeLoadBalance)
	}
	if _, err := gw.getStrategy(); err != nil {
		t.Fatalf("getStrategy on a legacy-spelled config: %v", err)
	}
}

func TestReloadConfig_NormalizesLegacyLoadBalanceMode(t *testing.T) {
	gw, err := New(config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "openai"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = gw.Close() })

	if err := gw.ReloadConfig(context.Background(), legacyLoadBalanceConfig()); err != nil {
		t.Fatalf("ReloadConfig: %v", err)
	}
	if got := gw.config.Strategy.Mode; got != config.ModeLoadBalance {
		t.Fatalf("installed mode after reload = %q, want %q", got, config.ModeLoadBalance)
	}
	if _, err := gw.getStrategy(); err != nil {
		t.Fatalf("getStrategy after reloading a legacy-spelled config: %v", err)
	}
}
