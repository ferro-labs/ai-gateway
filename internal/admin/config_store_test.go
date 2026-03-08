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
