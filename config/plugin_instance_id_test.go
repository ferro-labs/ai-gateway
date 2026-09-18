package config

import (
	"strings"
	"testing"
)

// TestValidatePluginInstanceIDs checks that a plugin id names a single
// configured instance: two different instances may not share one, while one
// instance listed across stages carries the same id.
func TestValidatePluginInstanceIDs(t *testing.T) {
	base := func(plugins []PluginConfig) Config {
		return Config{
			Strategy: StrategyConfig{Mode: ModeSingle},
			Targets:  []Target{{VirtualKey: "openai"}},
			Plugins:  plugins,
		}
	}
	p := func(name, id, stage string, cfg map[string]any) PluginConfig {
		return PluginConfig{Name: name, ID: id, Type: "guardrail", Stage: stage, Enabled: true, Config: cfg}
	}
	ruleA := map[string]any{"rules": []any{"a"}}
	ruleB := map[string]any{"rules": []any{"b"}}

	tests := []struct {
		name    string
		plugins []PluginConfig
		wantErr string
	}{
		{
			name:    "two different instances sharing one id is rejected",
			plugins: []PluginConfig{p("pii-redact", "dup", "before_request", ruleA), p("pii-redact", "dup", "before_request", ruleB)},
			wantErr: "used by more than one",
		},
		{
			name:    "distinct ids are fine",
			plugins: []PluginConfig{p("pii-redact", "ssn", "before_request", ruleA), p("pii-redact", "email", "before_request", ruleB)},
		},
		{
			name:    "one instance across two stages shares its id",
			plugins: []PluginConfig{p("response-cache", "c1", "before_request", ruleA), p("response-cache", "c1", "after_request", ruleA)},
		},
		{
			name:    "one instance across two stages under two ids is rejected",
			plugins: []PluginConfig{p("response-cache", "c1", "before_request", ruleA), p("response-cache", "c2", "after_request", ruleA)},
			wantErr: "carries two ids",
		},
		{
			name:    "an id longer than the maximum is rejected",
			plugins: []PluginConfig{p("pii-redact", strings.Repeat("x", maxPluginIDLen+1), "before_request", ruleA)},
			wantErr: "the maximum is 128",
		},
		{
			name:    "an id at the maximum is accepted",
			plugins: []PluginConfig{p("pii-redact", strings.Repeat("x", maxPluginIDLen), "before_request", ruleA)},
		},
		{
			// The limit is stated in characters, so it is counted in them: these
			// are 128 runes but 384 bytes, and len() would reject them.
			name:    "a multi-byte id at the maximum is accepted",
			plugins: []PluginConfig{p("pii-redact", strings.Repeat("é", maxPluginIDLen), "before_request", ruleA)},
		},
		{
			name:    "empty ids never collide",
			plugins: []PluginConfig{p("pii-redact", "", "before_request", ruleA), p("word-filter", "", "before_request", ruleB)},
		},
		{
			name:    "a disabled entry does not claim an id",
			plugins: []PluginConfig{p("pii-redact", "x", "before_request", ruleA), {Name: "word-filter", ID: "x", Type: "guardrail", Stage: "before_request", Config: ruleB}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateConfig(base(tt.plugins))
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("ValidateConfig() = %v, want nil", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("ValidateConfig() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}
