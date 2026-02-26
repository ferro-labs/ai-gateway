package aigateway

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig_Valid(t *testing.T) {
	data := `{
		"strategy": {"mode": "loadbalance"},
		"targets": [
			{"virtual_key": "openai-key", "weight": 0.7},
			{"virtual_key": "anthropic-key", "weight": 0.3}
		]
	}`
	path := writeTempFile(t, "config.json", data)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Strategy.Mode != ModeLoadBalance {
		t.Errorf("expected mode %q, got %q", ModeLoadBalance, cfg.Strategy.Mode)
	}
	if len(cfg.Targets) != 2 {
		t.Errorf("expected 2 targets, got %d", len(cfg.Targets))
	}
}

func TestLoadConfig_NonExistentFile(t *testing.T) {
	_, err := LoadConfig("/tmp/does-not-exist-config-12345.json")
	if err == nil {
		t.Fatal("expected error for non-existent file")
	}
}

func TestLoadConfig_InvalidJSON(t *testing.T) {
	path := writeTempFile(t, "bad.json", `{invalid`)

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestValidateConfig_Valid(t *testing.T) {
	cfg := Config{
		Strategy: StrategyConfig{Mode: ModeFallback},
		Targets:  []Target{{VirtualKey: "key1"}},
	}
	if err := ValidateConfig(cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateConfig_DefaultsToSingle(t *testing.T) {
	cfg := Config{
		Strategy: StrategyConfig{Mode: ""},
		Targets:  []Target{{VirtualKey: "key1"}},
	}
	if err := ValidateConfig(cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateConfig_EmptyTargets(t *testing.T) {
	cfg := Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  nil,
	}
	if err := ValidateConfig(cfg); err == nil {
		t.Fatal("expected error for empty targets")
	}
}

func TestValidateConfig_UnknownStrategy(t *testing.T) {
	cfg := Config{
		Strategy: StrategyConfig{Mode: "unknown"},
		Targets:  []Target{{VirtualKey: "key1"}},
	}
	if err := ValidateConfig(cfg); err == nil {
		t.Fatal("expected error for unknown strategy")
	}
}

func TestValidateConfig_InvalidWeights(t *testing.T) {
	tests := []struct {
		name    string
		targets []Target
	}{
		{
			name: "negative weight",
			targets: []Target{
				{VirtualKey: "a", Weight: -1},
				{VirtualKey: "b", Weight: 2},
			},
		},
		{
			name: "zero total weight",
			targets: []Target{
				{VirtualKey: "a", Weight: 0},
				{VirtualKey: "b", Weight: 0},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				Strategy: StrategyConfig{Mode: ModeLoadBalance},
				Targets:  tt.targets,
			}
			if err := ValidateConfig(cfg); err == nil {
				t.Fatal("expected error for invalid weights")
			}
		})
	}
}

func TestLoadConfig_YAML(t *testing.T) {
	data := `
strategy:
  mode: fallback
targets:
  - virtual_key: openai
  - virtual_key: anthropic
`
	path := writeTempFile(t, "config.yaml", data)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Strategy.Mode != ModeFallback {
		t.Errorf("expected mode %q, got %q", ModeFallback, cfg.Strategy.Mode)
	}
	if len(cfg.Targets) != 2 {
		t.Errorf("expected 2 targets, got %d", len(cfg.Targets))
	}
}

func TestLoadConfig_YML(t *testing.T) {
	data := `
strategy:
  mode: single
targets:
  - virtual_key: openai
`
	path := writeTempFile(t, "config.yml", data)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Strategy.Mode != ModeSingle {
		t.Errorf("expected mode %q, got %q", ModeSingle, cfg.Strategy.Mode)
	}
}

func TestLoadConfig_UnsupportedExtension(t *testing.T) {
	path := writeTempFile(t, "config.toml", "key = value")
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for unsupported extension")
	}
}

func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("writing temp file: %v", err)
	}
	return path
}
