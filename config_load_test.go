package aigateway

import (
	"os"
	"path/filepath"
	"testing"

	pubmcp "github.com/ferro-labs/ai-gateway/mcp"
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

func TestLoadConfig_CostOptimizedUnpricedStrategy(t *testing.T) {
	data := `{
		"strategy": {"mode": "cost-optimized", "unpriced_strategy": "skip"},
		"targets": [
			{"virtual_key": "openai-key"}
		]
	}`
	path := writeTempFile(t, "config.json", data)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Strategy.UnpricedStrategy != unpricedStrategySkip {
		t.Errorf("expected unpriced_strategy %q, got %q", unpricedStrategySkip, cfg.Strategy.UnpricedStrategy)
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

func TestValidateConfig_NewStrategyModes(t *testing.T) {
	tests := []struct {
		name string
		mode StrategyMode
	}{
		{name: "least-latency", mode: ModeLatency},
		{name: "cost-optimized", mode: ModeCostOptimized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				Strategy: StrategyConfig{Mode: tt.mode},
				Targets:  []Target{{VirtualKey: "key1"}},
			}
			if err := ValidateConfig(cfg); err != nil {
				t.Fatalf("unexpected error for mode %q: %v", tt.mode, err)
			}
		})
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

func TestValidateConfig_CostOptimizedUnpricedStrategy(t *testing.T) {
	tests := []struct {
		name              string
		unpricedStrategy  string
		wantValidationErr bool
	}{
		{name: "default"},
		{name: "fallback", unpricedStrategy: "fallback"},
		{name: "skip", unpricedStrategy: unpricedStrategySkip},
		{name: "allow", unpricedStrategy: unpricedStrategyAllow},
		{name: "invalid", unpricedStrategy: "free", wantValidationErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				Strategy: StrategyConfig{Mode: ModeCostOptimized, UnpricedStrategy: tt.unpricedStrategy},
				Targets:  []Target{{VirtualKey: "key1"}},
			}
			err := ValidateConfig(cfg)
			if tt.wantValidationErr && err == nil {
				t.Fatal("expected validation error")
			}
			if !tt.wantValidationErr && err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
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

func TestValidateConfig_PrivacyLevel(t *testing.T) {
	validLevels := []string{"", "none", "metadata", "full"}
	for _, level := range validLevels {
		cfg := Config{
			Strategy:      StrategyConfig{Mode: ModeSingle},
			Targets:       []Target{{VirtualKey: "key1"}},
			Observability: ObservabilityConfig{Tracing: TracingConfig{PrivacyLevel: level}},
		}
		if err := ValidateConfig(cfg); err != nil {
			t.Errorf("ValidateConfig with PrivacyLevel=%q: unexpected error: %v", level, err)
		}
	}

	invalidLevels := []string{"bogus", "FULL", "None", "ALL", "redact"}
	for _, level := range invalidLevels {
		cfg := Config{
			Strategy:      StrategyConfig{Mode: ModeSingle},
			Targets:       []Target{{VirtualKey: "key1"}},
			Observability: ObservabilityConfig{Tracing: TracingConfig{PrivacyLevel: level}},
		}
		if err := ValidateConfig(cfg); err == nil {
			t.Errorf("ValidateConfig with PrivacyLevel=%q: expected error, got nil", level)
		}
	}
}

func TestValidateConfig_MCPServerTransportSelection(t *testing.T) {
	base := Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "key1"}},
	}

	t.Run("url only is valid", func(t *testing.T) {
		cfg := base
		cfg.MCPServers = []pubmcp.ServerConfig{{Name: "http-server", URL: "https://mcp.example.com/mcp"}}
		if err := ValidateConfig(cfg); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("command only is valid", func(t *testing.T) {
		cfg := base
		cfg.MCPServers = []pubmcp.ServerConfig{{Name: "stdio-server", Command: "npx", Args: []string{"some-mcp-server"}}}
		if err := ValidateConfig(cfg); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("both url and command is invalid", func(t *testing.T) {
		cfg := base
		cfg.MCPServers = []pubmcp.ServerConfig{{Name: "ambiguous", URL: "https://mcp.example.com/mcp", Command: "npx"}}
		if err := ValidateConfig(cfg); err == nil {
			t.Error("expected error when both url and command are set, got nil")
		}
	})

	t.Run("neither url nor command is invalid", func(t *testing.T) {
		cfg := base
		cfg.MCPServers = []pubmcp.ServerConfig{{Name: "empty"}}
		if err := ValidateConfig(cfg); err == nil {
			t.Error("expected error when neither url nor command is set, got nil")
		}
	})
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
