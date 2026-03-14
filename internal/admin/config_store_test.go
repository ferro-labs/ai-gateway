package admin

import (
	"errors"
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
)

type failingConfigStore struct {
	saveErr error
}

func (s *failingConfigStore) Save(aigateway.Config) error { return s.saveErr }
func (s *failingConfigStore) Load() (aigateway.Config, bool, error) {
	return aigateway.Config{}, false, nil
}
func (s *failingConfigStore) Delete() error { return nil }

func TestGatewayConfigManager_ReloadConfig_RollsBackWhenSaveFails(t *testing.T) {
	initial := aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: "openai"}},
	}
	gw, err := aigateway.New(initial)
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}

	mgr, err := NewGatewayConfigManager(gw, &failingConfigStore{saveErr: errors.New("db down")})
	if err != nil {
		t.Fatalf("new config manager: %v", err)
	}

	next := aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeFallback},
		Targets:  []aigateway.Target{{VirtualKey: "openai"}, {VirtualKey: "anthropic"}},
	}
	err = mgr.ReloadConfig(next)
	if err == nil {
		t.Fatal("expected save failure")
	}
	if !errors.Is(err, errConfigPersistence) {
		t.Fatalf("expected persistence-classified error, got: %v", err)
	}

	got := mgr.GetConfig()
	if got.Strategy.Mode != initial.Strategy.Mode {
		t.Fatalf("expected rollback to initial mode %q, got %q", initial.Strategy.Mode, got.Strategy.Mode)
	}
	if len(got.Targets) != len(initial.Targets) {
		t.Fatalf("expected rollback target count %d, got %d", len(initial.Targets), len(got.Targets))
	}
}

func TestGatewayConfigManager_ReloadConfig_ClassifiesValidationErrors(t *testing.T) {
	initial := aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: "openai"}},
	}
	gw, err := aigateway.New(initial)
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	mgr, err := NewGatewayConfigManager(gw, nil)
	if err != nil {
		t.Fatalf("new config manager: %v", err)
	}

	invalid := aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: "invalid"},
		Targets:  []aigateway.Target{{VirtualKey: "openai"}},
	}
	err = mgr.ReloadConfig(invalid)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !errors.Is(err, errConfigValidation) {
		t.Fatalf("expected validation-classified error, got: %v", err)
	}
}

func TestGatewayConfigManager_NilGateway(t *testing.T) {
	_, err := NewGatewayConfigManager(nil, nil)
	if err == nil {
		t.Error("expected error for nil gateway")
	}
}

func TestGatewayConfigManager_NilStore(t *testing.T) {
	cfg := aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: "openai"}},
	}
	gw, err := aigateway.New(cfg)
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	mgr, err := NewGatewayConfigManager(gw, nil)
	if err != nil {
		t.Fatalf("NewGatewayConfigManager(nil store) failed: %v", err)
	}
	if mgr.GetConfig().Strategy.Mode != aigateway.ModeSingle {
		t.Error("expected single mode after creation with nil store")
	}
}

type successConfigStore struct {
	cfg aigateway.Config
}

func (s *successConfigStore) Save(c aigateway.Config) error { s.cfg = c; return nil }
func (s *successConfigStore) Load() (aigateway.Config, bool, error) {
	return s.cfg, s.cfg.Strategy.Mode != "", nil
}
func (s *successConfigStore) Delete() error { s.cfg = aigateway.Config{}; return nil }

func TestGatewayConfigManager_WithPersistedConfig(t *testing.T) {
	initial := aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: "openai"}},
	}
	persisted := aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeFallback},
		Targets:  []aigateway.Target{{VirtualKey: "openai"}, {VirtualKey: "anthropic"}},
	}
	store := &successConfigStore{cfg: persisted}

	gw, err := aigateway.New(initial)
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	mgr, err := NewGatewayConfigManager(gw, store)
	if err != nil {
		t.Fatalf("NewGatewayConfigManager with persisted config failed: %v", err)
	}
	if mgr.GetConfig().Strategy.Mode != aigateway.ModeFallback {
		t.Errorf("expected persisted mode %q to be applied, got %q", aigateway.ModeFallback, mgr.GetConfig().Strategy.Mode)
	}
}

func TestGatewayConfigManager_ReloadConfig_Success(t *testing.T) {
	initial := aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: "openai"}},
	}
	gw, err := aigateway.New(initial)
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	mgr, err := NewGatewayConfigManager(gw, nil)
	if err != nil {
		t.Fatalf("new config manager: %v", err)
	}

	next := aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeFallback},
		Targets:  []aigateway.Target{{VirtualKey: "openai"}, {VirtualKey: "anthropic"}},
	}
	if err := mgr.ReloadConfig(next); err != nil {
		t.Fatalf("ReloadConfig failed: %v", err)
	}
	if mgr.GetConfig().Strategy.Mode != aigateway.ModeFallback {
		t.Errorf("expected fallback mode after reload, got %q", mgr.GetConfig().Strategy.Mode)
	}
}

func TestGatewayConfigManager_ResetConfig(t *testing.T) {
	initial := aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: "openai"}},
	}
	gw, err := aigateway.New(initial)
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	store := &successConfigStore{}
	mgr, err := NewGatewayConfigManager(gw, store)
	if err != nil {
		t.Fatalf("new config manager: %v", err)
	}

	// Reload to a different config.
	next := aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeFallback},
		Targets:  []aigateway.Target{{VirtualKey: "openai"}, {VirtualKey: "anthropic"}},
	}
	if err := mgr.ReloadConfig(next); err != nil {
		t.Fatalf("ReloadConfig failed: %v", err)
	}

	// Reset to initial.
	if err := mgr.ResetConfig(); err != nil {
		t.Fatalf("ResetConfig failed: %v", err)
	}
	if mgr.GetConfig().Strategy.Mode != aigateway.ModeSingle {
		t.Errorf("expected reset to single mode, got %q", mgr.GetConfig().Strategy.Mode)
	}
}
