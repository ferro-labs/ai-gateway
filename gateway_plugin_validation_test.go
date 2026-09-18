package aigateway

import (
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"

	_ "github.com/ferro-labs/ai-gateway/plugin/regexguard"
)

// TestLoadPlugins_RejectsAmbiguousInstanceIDs covers the path ValidateConfig does
// not: LoadPlugins can be handed a plugin list that never went through it — an
// admin reload, or a list assembled in Go — so buildPluginManager runs the
// plugin-list rules itself. Without that, a direct load accepts two instances
// answering to one id and every decision they make is ambiguous.
func TestLoadPlugins_RejectsAmbiguousInstanceIDs(t *testing.T) {
	rule := func(pattern string) map[string]any {
		return map[string]any{"rules": []any{map[string]any{"pattern": pattern}}}
	}
	entry := func(id, pattern string) config.PluginConfig {
		return config.PluginConfig{
			Name: "regex-guard", ID: id, Type: "guardrail",
			Stage: "before_request", Enabled: true, Config: rule(pattern),
		}
	}

	tests := []struct {
		name    string
		plugins []config.PluginConfig
		wantErr string
	}{
		{
			name:    "two instances under one id",
			plugins: []config.PluginConfig{entry("dup", "a"), entry("dup", "b")},
			wantErr: "more than one plugin instance",
		},
		{
			name:    "an id past the maximum",
			plugins: []config.PluginConfig{entry(strings.Repeat("x", 129), "a")},
			wantErr: "the maximum is 128",
		},
		{
			name:    "distinct ids load",
			plugins: []config.PluginConfig{entry("first", "a"), entry("second", "b")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Built with a valid config, then handed the plugin list directly, so
			// the list reaches LoadPlugins without passing ValidateConfig.
			gw, err := newTestGateway(t, config.Config{
				Strategy: config.StrategyConfig{Mode: config.ModeSingle},
				Targets:  []config.Target{{VirtualKey: "openai"}},
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			gw.mu.Lock()
			gw.config.Plugins = tt.plugins
			gw.mu.Unlock()

			err = gw.LoadPlugins()
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("LoadPlugins() = %v, want nil", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("LoadPlugins() = %v, want an error containing %q", err, tt.wantErr)
			}
		})
	}
}
