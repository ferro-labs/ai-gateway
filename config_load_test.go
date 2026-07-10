package aigateway

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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

func TestLoadConfig_CircuitBreakerMaxHalfThreshold_JSON(t *testing.T) {
	data := `{
		"strategy": {"mode": "single"},
		"targets": [{
			"virtual_key": "openai",
			"circuit_breaker": {
				"failure_threshold": 5,
				"success_threshold": 2,
				"max_half_threshold": 3,
				"timeout": "30s"
			}
		}]
	}`
	path := writeTempFile(t, "config.json", data)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cb := cfg.Targets[0].CircuitBreaker
	if cb == nil {
		t.Fatal("expected CircuitBreaker config to be present")
	}
	if cb.MaxHalfThreshold != 3 {
		t.Errorf("MaxHalfThreshold = %d, want 3", cb.MaxHalfThreshold)
	}
	if cb.FailureThreshold != 5 {
		t.Errorf("FailureThreshold = %d, want 5", cb.FailureThreshold)
	}
}

func TestLoadConfig_CircuitBreakerMaxHalfThreshold_YAML(t *testing.T) {
	data := `
strategy:
  mode: single
targets:
  - virtual_key: anthropic
    circuit_breaker:
      failure_threshold: 3
      success_threshold: 1
      max_half_threshold: 2
      timeout: 15s
`
	path := writeTempFile(t, "config.yaml", data)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cb := cfg.Targets[0].CircuitBreaker
	if cb == nil {
		t.Fatal("expected CircuitBreaker config to be present")
	}
	if cb.MaxHalfThreshold != 2 {
		t.Errorf("MaxHalfThreshold = %d, want 2", cb.MaxHalfThreshold)
	}
}

func TestLoadConfig_RejectsUnknownKey(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		data    string
		wantKey string
	}{
		{
			name:    "yaml top-level typo",
			file:    "config.yaml",
			data:    "strategy:\n  mode: single\ntargetz:\n  - virtual_key: openai\n",
			wantKey: "targetz",
		},
		{
			name:    "yaml nested typo",
			file:    "config.yaml",
			data:    "strategy:\n  modee: single\ntargets:\n  - virtual_key: openai\n",
			wantKey: "modee",
		},
		{
			name:    "json top-level typo",
			file:    "config.json",
			data:    `{"strategy":{"mode":"single"},"targetz":[{"virtual_key":"openai"}]}`,
			wantKey: "targetz",
		},
		{
			name:    "json nested typo",
			file:    "config.json",
			data:    `{"strategy":{"modee":"single"},"targets":[{"virtual_key":"openai"}]}`,
			wantKey: "modee",
		},
		{
			// Underscore-prefixed keys against the typed schema are ordinary
			// unknown keys, not tolerated pseudo-comments.
			name:    "json underscore-prefixed top-level key",
			file:    "config.json",
			data:    `{"_note":"x","strategy":{"mode":"single"},"targets":[{"virtual_key":"openai"}]}`,
			wantKey: "_note",
		},
		{
			name:    "yaml underscore-prefixed top-level key",
			file:    "config.yaml",
			data:    "_note: x\nstrategy:\n  mode: single\ntargets:\n  - virtual_key: openai\n",
			wantKey: "_note",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTempFile(t, tt.file, tt.data)
			_, err := LoadConfig(path)
			if err == nil {
				t.Fatalf("expected error for unknown key %q, got nil", tt.wantKey)
			}
			if !strings.Contains(err.Error(), tt.wantKey) {
				t.Errorf("error %q should name the unknown key %q", err, tt.wantKey)
			}
		})
	}
}

func TestLoadConfig_PreservesUnderscoreKeysInPluginConfig(t *testing.T) {
	// A plugin's free-form "config" map accepts arbitrary keys, including
	// underscore-prefixed ones a plugin may use (e.g. "_token_source"). Strict
	// decoding must not strip them: they have to reach the plugin unchanged.
	tests := []struct {
		name string
		file string
		data string
	}{
		{
			name: "json",
			file: "config.json",
			data: `{
				"strategy": {"mode": "single"},
				"targets": [{"virtual_key": "openai"}],
				"plugins": [{
					"name": "request-logger",
					"type": "logging",
					"stage": "before_request",
					"enabled": true,
					"config": {"_x": "y", "level": "info"}
				}]
			}`,
		},
		{
			name: "yaml",
			file: "config.yaml",
			data: "strategy:\n  mode: single\n" +
				"targets:\n  - virtual_key: openai\n" +
				"plugins:\n  - name: request-logger\n    type: logging\n" +
				"    stage: before_request\n    enabled: true\n" +
				"    config:\n      _x: y\n      level: info\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTempFile(t, tt.file, tt.data)
			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(cfg.Plugins) != 1 {
				t.Fatalf("expected 1 plugin, got %d", len(cfg.Plugins))
			}
			if got := cfg.Plugins[0].Config["_x"]; got != "y" {
				t.Errorf("plugin config[_x] = %v, want \"y\"", got)
			}
		})
	}
}

func TestLoadConfig_YAMLRejectsSecondDocument(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		wantErr bool
	}{
		{
			name: "single valid document loads",
			data: "strategy:\n  mode: single\ntargets:\n  - virtual_key: openai\n",
		},
		{
			name:    "trailing second document rejected",
			data:    "strategy:\n  mode: single\ntargets:\n  - virtual_key: openai\n---\nstrategy:\n  mode: fallback\n",
			wantErr: true,
		},
		{
			name:    "trailing malformed second document rejected",
			data:    "strategy:\n  mode: single\ntargets:\n  - virtual_key: openai\n---\n[unterminated\n",
			wantErr: true,
		},
		{
			name: "empty document still tolerated",
			data: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTempFile(t, "config.yaml", tt.data)
			_, err := LoadConfig(path)
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestLoadConfig_JSONRejectsTrailingData(t *testing.T) {
	// A second top-level value after the config object is rejected, matching a
	// whole-document json.Unmarshal.
	data := `{"strategy":{"mode":"single"},"targets":[{"virtual_key":"openai"}]}{"extra":true}`
	path := writeTempFile(t, "config.json", data)
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected error for trailing JSON data, got nil")
	}
}

func TestLoadConfig_ExampleFilesParse(t *testing.T) {
	// The shipped example configs must survive strict decoding and validation.
	for _, name := range []string{"config.example.yaml", "config.example.json"} {
		t.Run(name, func(t *testing.T) {
			cfg, err := LoadConfig(name)
			if err != nil {
				t.Fatalf("LoadConfig(%q) failed: %v", name, err)
			}
			if err := ValidateConfig(*cfg); err != nil {
				t.Fatalf("ValidateConfig(%q) failed: %v", name, err)
			}
		})
	}
}

func TestNormalize_AppliesDefaults(t *testing.T) {
	cfg := Config{Targets: []Target{{VirtualKey: "openai"}}}
	cfg.Normalize()
	if cfg.Strategy.Mode != ModeSingle {
		t.Errorf("Strategy.Mode = %q, want %q", cfg.Strategy.Mode, ModeSingle)
	}
	if cfg.APIVersion != CurrentAPIVersion {
		t.Errorf("APIVersion = %q, want %q", cfg.APIVersion, CurrentAPIVersion)
	}

	// Idempotent and non-clobbering: an explicit mode survives a second pass.
	cfg.Strategy.Mode = ModeFallback
	cfg.Normalize()
	if cfg.Strategy.Mode != ModeFallback {
		t.Errorf("Normalize overwrote explicit mode: got %q", cfg.Strategy.Mode)
	}
}

func TestLoadConfig_APIVersion(t *testing.T) {
	tests := []struct {
		name       string
		apiVersion string // "" means field omitted
		wantStored string
		wantWarn   bool
	}{
		{name: "omitted defaults to current", apiVersion: "", wantStored: CurrentAPIVersion, wantWarn: false},
		{name: "known value accepted", apiVersion: CurrentAPIVersion, wantStored: CurrentAPIVersion, wantWarn: false},
		{name: "unknown value warns but loads", apiVersion: "v2", wantStored: "v2", wantWarn: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureSlog(t)

			data := `{"strategy":{"mode":"single"},"targets":[{"virtual_key":"openai"}]`
			if tt.apiVersion != "" {
				data += `,"apiVersion":"` + tt.apiVersion + `"`
			}
			data += `}`
			path := writeTempFile(t, "config.json", data)

			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.APIVersion != tt.wantStored {
				t.Errorf("APIVersion = %q, want %q", cfg.APIVersion, tt.wantStored)
			}
			gotWarn := strings.Contains(logs.String(), "apiVersion")
			if gotWarn != tt.wantWarn {
				t.Errorf("warning emitted = %v, want %v (logs: %q)", gotWarn, tt.wantWarn, logs.String())
			}
		})
	}
}

// captureSlog redirects the default slog logger to a buffer for the duration of
// the test and restores it afterwards.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
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
