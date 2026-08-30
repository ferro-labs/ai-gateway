package config_test

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/pkg/logger"

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

	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Strategy.Mode != config.ModeLoadBalance {
		t.Errorf("expected mode %q, got %q", config.ModeLoadBalance, cfg.Strategy.Mode)
	}
	if len(cfg.Targets) != 2 {
		t.Errorf("expected 2 targets, got %d", len(cfg.Targets))
	}
}

func TestLoadConfig_TargetModelMap(t *testing.T) {
	tests := []struct {
		name string
		ext  string
		data string
	}{
		{name: "JSON", ext: "json", data: `{"strategy":{"mode":"single"},"targets":[{"virtual_key":"openai","model_map":{"support-chat":"gpt-4o"}}]}`},
		{name: "YAML", ext: "yaml", data: "strategy:\n  mode: single\ntargets:\n  - virtual_key: openai\n    model_map:\n      support-chat: gpt-4o\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := config.LoadConfig(writeTempFile(t, "config."+tt.ext, tt.data))
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if got := cfg.Targets[0].ModelMap["support-chat"]; got != "gpt-4o" {
				t.Errorf("model_map[support-chat] = %q, want gpt-4o", got)
			}
		})
	}
}

func TestLoadConfig_RejectsDuplicateModelMapKeys(t *testing.T) {
	tests := []struct {
		name string
		ext  string
		data string
	}{
		{name: "JSON", ext: "json", data: `{"targets":[{"virtual_key":"openai","model_map":{"support-chat":"gpt-4o","support-chat":"gpt-4o-mini"}}]}`},
		{name: "YAML", ext: "yaml", data: "targets:\n  - virtual_key: openai\n    model_map:\n      support-chat: gpt-4o\n      support-chat: gpt-4o-mini\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := config.LoadConfig(writeTempFile(t, "config."+tt.ext, tt.data))
			if err == nil {
				t.Fatal("expected duplicate model_map key to be rejected")
			}
			if !strings.Contains(err.Error(), "support-chat") {
				t.Fatalf("error %q should identify duplicate key support-chat", err)
			}
		})
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

	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Strategy.UnpricedStrategy != config.UnpricedStrategySkip {
		t.Errorf("expected unpriced_strategy %q, got %q", config.UnpricedStrategySkip, cfg.Strategy.UnpricedStrategy)
	}
}

func TestLoadConfig_NonExistentFile(t *testing.T) {
	_, err := config.LoadConfig("/tmp/does-not-exist-config-12345.json")
	if err == nil {
		t.Fatal("expected error for non-existent file")
	}
}

func TestLoadConfig_InvalidJSON(t *testing.T) {
	path := writeTempFile(t, "bad.json", `{invalid`)

	_, err := config.LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestValidateConfig_Valid(t *testing.T) {
	cfg := config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets:  []config.Target{{VirtualKey: "key1"}},
	}
	if err := config.ValidateConfig(cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateConfig_NewStrategyModes(t *testing.T) {
	tests := []struct {
		name string
		mode config.StrategyMode
	}{
		{name: "least-latency", mode: config.ModeLatency},
		{name: "cost-optimized", mode: config.ModeCostOptimized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Config{
				Strategy: config.StrategyConfig{Mode: tt.mode},
				Targets:  []config.Target{{VirtualKey: "key1"}},
			}
			if err := config.ValidateConfig(cfg); err != nil {
				t.Fatalf("unexpected error for mode %q: %v", tt.mode, err)
			}
		})
	}
}

func TestValidateConfig_DefaultsToSingle(t *testing.T) {
	cfg := config.Config{
		Strategy: config.StrategyConfig{Mode: ""},
		Targets:  []config.Target{{VirtualKey: "key1"}},
	}
	if err := config.ValidateConfig(cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateConfig_EmptyTargets(t *testing.T) {
	cfg := config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  nil,
	}
	if err := config.ValidateConfig(cfg); err == nil {
		t.Fatal("expected error for empty targets")
	}
}

func TestValidateConfig_UnknownStrategy(t *testing.T) {
	cfg := config.Config{
		Strategy: config.StrategyConfig{Mode: "unknown"},
		Targets:  []config.Target{{VirtualKey: "key1"}},
	}
	if err := config.ValidateConfig(cfg); err == nil {
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
		{name: "skip", unpricedStrategy: config.UnpricedStrategySkip},
		{name: "allow", unpricedStrategy: config.UnpricedStrategyAllow},
		{name: "invalid", unpricedStrategy: "free", wantValidationErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Config{
				Strategy: config.StrategyConfig{Mode: config.ModeCostOptimized, UnpricedStrategy: tt.unpricedStrategy},
				Targets:  []config.Target{{VirtualKey: "key1"}},
			}
			err := config.ValidateConfig(cfg)
			if tt.wantValidationErr && err == nil {
				t.Fatal("expected validation error")
			}
			if !tt.wantValidationErr && err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}

func TestValidateConfig_CompatibilityOnUnsupportedParam(t *testing.T) {
	tests := []struct {
		name              string
		mode              string
		wantValidationErr bool
	}{
		{name: "default empty"},
		{name: "warn", mode: "warn"},
		{name: "drop", mode: "drop"},
		{name: "reject", mode: "reject"},
		{name: "invalid", mode: "explode", wantValidationErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Config{
				Strategy:      config.StrategyConfig{Mode: config.ModeSingle},
				Targets:       []config.Target{{VirtualKey: "key1"}},
				Compatibility: config.CompatibilityConfig{OnUnsupportedParam: tt.mode},
			}
			err := config.ValidateConfig(cfg)
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
		targets []config.Target
	}{
		{
			name: "negative weight",
			targets: []config.Target{
				{VirtualKey: "a", Weight: -1},
				{VirtualKey: "b", Weight: 2},
			},
		},
		{
			name: "zero total weight",
			targets: []config.Target{
				{VirtualKey: "a", Weight: 0},
				{VirtualKey: "b", Weight: 0},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Config{
				Strategy: config.StrategyConfig{Mode: config.ModeLoadBalance},
				Targets:  tt.targets,
			}
			if err := config.ValidateConfig(cfg); err == nil {
				t.Fatal("expected error for invalid weights")
			}
		})
	}
}

func TestValidateConfig_ConcurrencyBounds(t *testing.T) {
	tests := []struct {
		name        string
		concurrency *config.ConcurrencyConfig
		wantErr     bool
	}{
		{name: "nil concurrency is unlimited", concurrency: nil, wantErr: false},
		{name: "positive max_concurrency", concurrency: &config.ConcurrencyConfig{MaxConcurrency: 10}, wantErr: false},
		{name: "zero max_concurrency rejected", concurrency: &config.ConcurrencyConfig{MaxConcurrency: 0}, wantErr: true},
		{name: "negative max_concurrency rejected", concurrency: &config.ConcurrencyConfig{MaxConcurrency: -1}, wantErr: true},
		{name: "absurd max_concurrency rejected", concurrency: &config.ConcurrencyConfig{MaxConcurrency: 100_000_000}, wantErr: true},
		{name: "negative queue_size rejected", concurrency: &config.ConcurrencyConfig{MaxConcurrency: 10, QueueSize: -1}, wantErr: true},
		{name: "absurd queue_size rejected", concurrency: &config.ConcurrencyConfig{MaxConcurrency: 10, QueueSize: 100_000_000}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Config{
				Strategy: config.StrategyConfig{Mode: config.ModeSingle},
				Targets:  []config.Target{{VirtualKey: "key1", Concurrency: tt.concurrency}},
			}
			err := config.ValidateConfig(cfg)
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
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
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Strategy.Mode != config.ModeFallback {
		t.Errorf("expected mode %q, got %q", config.ModeFallback, cfg.Strategy.Mode)
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
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Strategy.Mode != config.ModeSingle {
		t.Errorf("expected mode %q, got %q", config.ModeSingle, cfg.Strategy.Mode)
	}
}

func TestLoadConfig_UnsupportedExtension(t *testing.T) {
	path := writeTempFile(t, "config.toml", "key = value")
	_, err := config.LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for unsupported extension")
	}
}

func TestValidateConfig_PrivacyLevel(t *testing.T) {
	validLevels := []string{"", "none", "metadata", "full"}
	for _, level := range validLevels {
		cfg := config.Config{
			Strategy:      config.StrategyConfig{Mode: config.ModeSingle},
			Targets:       []config.Target{{VirtualKey: "key1"}},
			Observability: config.ObservabilityConfig{Tracing: config.TracingConfig{PrivacyLevel: level}},
		}
		if err := config.ValidateConfig(cfg); err != nil {
			t.Errorf("ValidateConfig with PrivacyLevel=%q: unexpected error: %v", level, err)
		}
	}

	invalidLevels := []string{"bogus", "FULL", "None", "ALL", "redact"}
	for _, level := range invalidLevels {
		cfg := config.Config{
			Strategy:      config.StrategyConfig{Mode: config.ModeSingle},
			Targets:       []config.Target{{VirtualKey: "key1"}},
			Observability: config.ObservabilityConfig{Tracing: config.TracingConfig{PrivacyLevel: level}},
		}
		if err := config.ValidateConfig(cfg); err == nil {
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
	cfg, err := config.LoadConfig(path)
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
	cfg, err := config.LoadConfig(path)
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
			_, err := config.LoadConfig(path)
			if err == nil {
				t.Fatalf("expected error for unknown key %q, got nil", tt.wantKey)
			}
			if !strings.Contains(err.Error(), tt.wantKey) {
				t.Errorf("error %q should name the unknown key %q", err, tt.wantKey)
			}
		})
	}
}

// TestLoadConfig_ReportsEveryUnknownKey pins the accumulating half of strict
// decoding, in BOTH formats: a config with several typos names them ALL in one
// error, so fixing it is one edit rather than one restart per typo.
//
// YAML got this for free — its decoder collects every unknown field into a
// single TypeError — and JSON did not, because encoding/json's
// DisallowUnknownFields returns on the first one. A JSON config with three
// typos therefore cost three restarts to correct. Both are behaviour rather
// than documented API, so both are asserted rather than assumed.
func TestLoadConfig_ReportsEveryUnknownKey(t *testing.T) {
	tests := []struct {
		name string
		file string
		data string
	}{
		{
			name: "yaml",
			file: "config.yaml",
			data: "targetz: []\nstrategyy: {}\npluginz: []\ntargets:\n  - virtual_key: openai\n",
		},
		{
			name: "json",
			file: "config.json",
			data: `{"targetz":[],"strategyy":{},"pluginz":[],` +
				`"targets":[{"virtual_key":"openai"}]}`,
		},
		{
			// Nested typos count too: an unknown key inside targets[] is
			// exactly as costly to find one restart at a time.
			name: "json nested",
			file: "config.json",
			data: `{"strategy":{"modee":"single"},"aliasez":{},` +
				`"targets":[{"virtual_key":"openai","model":["gpt-4o-mini"],"weightt":1}]}`,
		},
	}

	want := map[string][]string{
		"yaml":        {"targetz", "strategyy", "pluginz"},
		"json":        {"targetz", "strategyy", "pluginz"},
		"json nested": {"modee", "aliasez", "model", "weightt"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTempFile(t, tt.file, tt.data)
			_, err := config.LoadConfig(path)
			if err == nil {
				t.Fatal("expected an error for unknown keys, got nil")
			}
			for _, key := range want[tt.name] {
				if !strings.Contains(err.Error(), key) {
					t.Errorf("error %q should name every unknown key; missing %q", err, key)
				}
			}
		})
	}
}

// TestLoadConfig_JSONKeepsStdlibErrorForNonSchemaFailures holds the enumeration
// to the one case it is for. A type mismatch and malformed JSON are reported by
// encoding/json in its own wording; only an unknown key takes the second pass,
// so a wrong value type is never relabelled as a wrong key.
func TestLoadConfig_JSONKeepsStdlibErrorForNonSchemaFailures(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{
			name: "type mismatch",
			data: `{"strategy":{"mode":"single"},"targets":[{"virtual_key":123}]}`,
			want: "cannot unmarshal",
		},
		{
			name: "malformed json",
			data: `{"targets":[`,
			want: "unexpected EOF",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTempFile(t, "config.json", tt.data)
			_, err := config.LoadConfig(path)
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q should keep the stdlib wording %q", err, tt.want)
			}
			if strings.Contains(err.Error(), "unknown field") {
				t.Errorf("error %q reports an unknown field for a %s", err, tt.name)
			}
		})
	}
}

// TestLoadConfig_JSONValueSemanticsAreUnchanged is the other half of the same
// rule: the YAML pass exists only to enumerate keys on a failing document and
// must never touch a valid one. A JSON number in a plugin's free-form config
// arrives as float64 because encoding/json decoded it; YAML would have made it
// an int, so an int here would mean the second decoder had quietly become the
// one producing values.
func TestLoadConfig_JSONValueSemanticsAreUnchanged(t *testing.T) {
	path := writeTempFile(t, "config.json", `{
		"strategy": {"mode": "single"},
		"targets": [{"virtual_key": "openai"}],
		"plugins": [{
			"name": "max-token",
			"type": "guardrail",
			"stage": "before_request",
			"enabled": true,
			"config": {"max_tokens": 4096}
		}]
	}`)

	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Plugins) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(cfg.Plugins))
	}
	got, ok := cfg.Plugins[0].Config["max_tokens"].(float64)
	if !ok {
		t.Fatalf("plugin config number is %T, want float64 (encoding/json semantics)", cfg.Plugins[0].Config["max_tokens"])
	}
	if got != 4096 {
		t.Errorf("max_tokens = %v, want 4096", got)
	}
}

// TestConfigSchemaTagsMatchAcrossFormats is the premise the JSON unknown-key
// enumeration rests on: it decodes the document a second time as YAML to list
// the keys encoding/json stopped at, which only resolves the same fields while
// every json tag has an identical yaml tag. A field given one and not the other
// would be reported as unknown when it is not — a false rejection message,
// which is worse than the one-key-at-a-time behaviour this replaced.
func TestConfigSchemaTagsMatchAcrossFormats(t *testing.T) {
	seen := map[reflect.Type]bool{}

	var walk func(rt reflect.Type, path string)
	walk = func(rt reflect.Type, path string) {
		for rt.Kind() == reflect.Ptr || rt.Kind() == reflect.Slice ||
			rt.Kind() == reflect.Array || rt.Kind() == reflect.Map {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct || seen[rt] {
			return
		}
		seen[rt] = true
		for i := range rt.NumField() {
			f := rt.Field(i)
			jsonTag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			yamlTag, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
			if jsonTag != yamlTag {
				t.Errorf("%s.%s: json tag %q != yaml tag %q", path, f.Name, jsonTag, yamlTag)
			}
			walk(f.Type, path+"."+f.Name)
		}
	}

	walk(reflect.TypeOf(config.Config{}), "Config")
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
			cfg, err := config.LoadConfig(path)
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
			_, err := config.LoadConfig(path)
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
	if _, err := config.LoadConfig(path); err == nil {
		t.Fatal("expected error for trailing JSON data, got nil")
	}
}

func TestLoadConfig_ExampleFilesParse(t *testing.T) {
	// The shipped example configs must survive strict decoding and validation.
	for _, name := range []string{"config.example.yaml", "config.example.json"} {
		t.Run(name, func(t *testing.T) {
			// The example configs live at the repo root, one level up from this package.
			cfg, err := config.LoadConfig(filepath.Join("..", name))
			if err != nil {
				t.Fatalf("LoadConfig(%q) failed: %v", name, err)
			}
			if err := config.ValidateConfig(*cfg); err != nil {
				t.Fatalf("ValidateConfig(%q) failed: %v", name, err)
			}
		})
	}
}

func TestValidateConfig_RequestTimeout(t *testing.T) {
	tests := []struct {
		name    string
		timeout string
		wantErr bool
	}{
		{"omitted is valid", "", false},
		{"valid duration", "30s", false},
		{"sub-second duration", "500ms", false},
		{"missing unit is rejected", "30", true},
		{"unparseable value is rejected", "soon", true},
		{"zero is rejected", "0s", true},
		{"negative is rejected", "-5s", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Config{
				Targets:        []config.Target{{VirtualKey: "openai"}},
				RequestTimeout: tt.timeout,
			}
			err := config.ValidateConfig(cfg)
			if tt.wantErr && err == nil {
				t.Errorf("request_timeout %q: expected an error, got nil", tt.timeout)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("request_timeout %q: unexpected error: %v", tt.timeout, err)
			}
		})
	}
}

func TestNormalize_AppliesDefaults(t *testing.T) {
	cfg := config.Config{Targets: []config.Target{{VirtualKey: "openai"}}}
	cfg.Normalize()
	if cfg.Strategy.Mode != config.ModeSingle {
		t.Errorf("Strategy.Mode = %q, want %q", cfg.Strategy.Mode, config.ModeSingle)
	}
	if cfg.APIVersion != config.CurrentAPIVersion {
		t.Errorf("APIVersion = %q, want %q", cfg.APIVersion, config.CurrentAPIVersion)
	}

	// Idempotent and non-clobbering: an explicit mode survives a second pass.
	cfg.Strategy.Mode = config.ModeFallback
	cfg.Normalize()
	if cfg.Strategy.Mode != config.ModeFallback {
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
		{name: "omitted defaults to current", apiVersion: "", wantStored: config.CurrentAPIVersion, wantWarn: false},
		{name: "known value accepted", apiVersion: config.CurrentAPIVersion, wantStored: config.CurrentAPIVersion, wantWarn: false},
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

			cfg, err := config.LoadConfig(path)
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

// captureSlog redirects the default logger to a buffer for the duration of the
// test and restores it afterwards.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := logger.Default()
	logger.SetDefault(logger.New(logger.Options{Output: buf}))
	t.Cleanup(func() { logger.SetDefault(prev) })
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

func TestValidateConfig_MCPServerTransportSelection(t *testing.T) {
	base := config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "key1"}},
	}

	t.Run("url only is valid", func(t *testing.T) {
		cfg := base
		cfg.MCPServers = []pubmcp.ServerConfig{{Name: "http-server", URL: "https://mcp.example.com/mcp"}}
		if err := config.ValidateConfig(cfg); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("command only is valid", func(t *testing.T) {
		cfg := base
		cfg.MCPServers = []pubmcp.ServerConfig{{Name: "stdio-server", Command: "npx", Args: []string{"some-mcp-server"}}}
		if err := config.ValidateConfig(cfg); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("both url and command is invalid", func(t *testing.T) {
		cfg := base
		cfg.MCPServers = []pubmcp.ServerConfig{{Name: "ambiguous", URL: "https://mcp.example.com/mcp", Command: "npx"}}
		if err := config.ValidateConfig(cfg); err == nil {
			t.Error("expected error when both url and command are set, got nil")
		}
	})

	t.Run("neither url nor command is invalid", func(t *testing.T) {
		cfg := base
		cfg.MCPServers = []pubmcp.ServerConfig{{Name: "empty"}}
		if err := config.ValidateConfig(cfg); err == nil {
			t.Error("expected error when neither url nor command is set, got nil")
		}
	})
}

func TestValidateMCPServers_NameRequiredAndUnique(t *testing.T) {
	base := config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "openai"}},
	}

	t.Run("empty name is rejected", func(t *testing.T) {
		cfg := base
		cfg.MCPServers = []pubmcp.ServerConfig{{URL: "https://mcp.example.com/mcp"}}
		if err := config.ValidateConfig(cfg); err == nil {
			t.Error("expected an error for an unnamed mcp server, got nil")
		}
	})

	t.Run("whitespace-only name is rejected", func(t *testing.T) {
		cfg := base
		cfg.MCPServers = []pubmcp.ServerConfig{{Name: "   ", URL: "https://mcp.example.com/mcp"}}
		if err := config.ValidateConfig(cfg); err == nil {
			t.Error("expected an error for a whitespace-only mcp server name, got nil")
		}
	})

	t.Run("duplicate names are rejected", func(t *testing.T) {
		cfg := base
		cfg.MCPServers = []pubmcp.ServerConfig{
			{Name: "tools", URL: "https://a.example.com/mcp"},
			{Name: "tools", URL: "https://b.example.com/mcp"},
		}
		err := config.ValidateConfig(cfg)
		if err == nil {
			t.Fatal("expected an error for duplicate mcp server names, got nil")
		}
		if !strings.Contains(err.Error(), "tools") {
			t.Errorf("error %q should name the duplicated server", err)
		}
	})

	t.Run("distinct names are accepted", func(t *testing.T) {
		cfg := base
		cfg.MCPServers = []pubmcp.ServerConfig{
			{Name: "a", URL: "https://a.example.com/mcp"},
			{Name: "b", Command: "npx"},
		}
		if err := config.ValidateConfig(cfg); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

// TestValidateConfig_TargetRetryBounds covers a block validation used to ignore
// entirely: retry is normalised at request time (attempts <= 0 becomes one
// attempt, backoff <= 0 becomes 100 ms), so a negative written by hand was
// silently replaced by a default the operator did not ask for and `ferrogw
// validate` reported the config green.
func TestValidateConfig_TargetRetryBounds(t *testing.T) {
	tests := []struct {
		name    string
		retry   *config.RetryConfig
		wantErr bool
	}{
		{name: "nil retry", retry: nil},
		{name: "attempts and backoff set", retry: &config.RetryConfig{Attempts: 3, InitialBackoffMs: 200}},
		{name: "zero is an omitted field", retry: &config.RetryConfig{}},
		{name: "negative attempts rejected", retry: &config.RetryConfig{Attempts: -1}, wantErr: true},
		{name: "negative backoff rejected", retry: &config.RetryConfig{Attempts: 2, InitialBackoffMs: -50}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Config{
				Strategy: config.StrategyConfig{Mode: config.ModeSingle},
				Targets:  []config.Target{{VirtualKey: "key1", Retry: tt.retry}},
			}
			err := config.ValidateConfig(cfg)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateConfig error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestValidateConfig_TargetCircuitBreakerBounds covers the other half of the
// same gap. circuitbreaker.New reads an unparseable timeout as zero and
// substitutes 30s, so a target configured to stay open for "5 minutes" reopened
// every 30 seconds with nothing but a startup warning to say so.
func TestValidateConfig_TargetCircuitBreakerBounds(t *testing.T) {
	tests := []struct {
		name    string
		cb      *config.CircuitBreakerConfig
		wantErr bool
	}{
		{name: "nil circuit breaker", cb: nil},
		{name: "fully configured", cb: &config.CircuitBreakerConfig{FailureThreshold: 5, SuccessThreshold: 2, MaxHalfThreshold: 1, Timeout: "30s"}},
		{name: "empty timeout takes the default", cb: &config.CircuitBreakerConfig{FailureThreshold: 5}},
		{name: "unparseable timeout rejected", cb: &config.CircuitBreakerConfig{Timeout: "banana"}, wantErr: true},
		{name: "unit-less timeout rejected", cb: &config.CircuitBreakerConfig{Timeout: "30"}, wantErr: true},
		{name: "prose timeout rejected", cb: &config.CircuitBreakerConfig{Timeout: "5 minutes"}, wantErr: true},
		{name: "negative timeout rejected", cb: &config.CircuitBreakerConfig{Timeout: "-30s"}, wantErr: true},
		{name: "negative failure_threshold rejected", cb: &config.CircuitBreakerConfig{FailureThreshold: -1}, wantErr: true},
		{name: "negative success_threshold rejected", cb: &config.CircuitBreakerConfig{SuccessThreshold: -1}, wantErr: true},
		{name: "negative max_half_threshold rejected", cb: &config.CircuitBreakerConfig{MaxHalfThreshold: -1}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Config{
				Strategy: config.StrategyConfig{Mode: config.ModeSingle},
				Targets:  []config.Target{{VirtualKey: "key1", CircuitBreaker: tt.cb}},
			}
			err := config.ValidateConfig(cfg)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateConfig error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestValidateMultiStagePlugins_DuplicateEntries covers the case the
// cross-stage predicate let through: two identical entries at ONE stage share
// an instance, so the plugin is registered twice at that stage and runs twice
// per request — a limiter spending two tokens, a budget billing twice.
//
// Entries that differ in config at one stage stay legal: those are two
// instances by design, which is how two limiters with different ceilings are
// written.
func TestValidateMultiStagePlugins_DuplicateEntries(t *testing.T) {
	entry := func(stage string, cfg map[string]any) config.PluginConfig {
		return config.PluginConfig{Name: "rate-limit", Stage: stage, Enabled: true, Config: cfg}
	}
	tight := map[string]any{"requests_per_second": 10}
	loose := map[string]any{"requests_per_second": 20}

	tests := []struct {
		name    string
		plugins []config.PluginConfig
		wantErr bool
	}{
		{
			name:    "identical entries at one stage rejected",
			plugins: []config.PluginConfig{entry("before_request", tight), entry("before_request", tight)},
			wantErr: true,
		},
		{
			name:    "identical entries at one stage rejected alongside a shared later stage",
			plugins: []config.PluginConfig{entry("before_request", tight), entry("before_request", tight), entry("after_request", tight)},
			wantErr: true,
		},
		{
			name:    "different configs at one stage remain two instances",
			plugins: []config.PluginConfig{entry("before_request", tight), entry("before_request", loose)},
		},
		{
			name:    "one entry per stage with identical config",
			plugins: []config.PluginConfig{entry("before_request", tight), entry("after_request", tight)},
		},
		{
			name:    "disagreeing configs across stages still rejected",
			plugins: []config.PluginConfig{entry("before_request", tight), entry("after_request", loose)},
			wantErr: true,
		},
		{
			name:    "a disabled duplicate is not a duplicate",
			plugins: []config.PluginConfig{entry("before_request", tight), {Name: "rate-limit", Stage: "before_request", Config: tight}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := config.ValidateMultiStagePlugins(tt.plugins)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateMultiStagePlugins error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
