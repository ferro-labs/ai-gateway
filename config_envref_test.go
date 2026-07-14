package aigateway

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/plugin"
)

// envrefProbe captures the config map it is Init'd with, so a test can prove the
// plugin receives the RESOLVED secret even though the Config only ever held ${VAR}.
type envrefProbe struct{}

var envrefProbeConfig = make(chan map[string]any, 1)

func (*envrefProbe) Name() string            { return "envref-probe" }
func (*envrefProbe) Type() plugin.PluginType { return plugin.TypeLogging }
func (*envrefProbe) Init(config map[string]any) error {
	select {
	case envrefProbeConfig <- config:
	default:
	}
	return nil
}
func (*envrefProbe) Execute(context.Context, *plugin.Context) error { return nil }
func (*envrefProbe) Close() error                                   { return nil }

func init() {
	plugin.RegisterFactory("envref-probe", func() plugin.Plugin { return &envrefProbe{} })
}

// TestLoadConfig_KeepsEnvReferencesUnresolved is the guard for the secret-at-rest
// leak. The Config travels a long way: it is persisted to the config-history store,
// returned by GET /admin/config, and re-saved on rollback. If LoadConfig materialised
// secrets into it, a failed admin config apply would write real API keys into the
// database in plaintext. References must survive in the Config; only the constructed
// component ever sees the value.
func TestLoadConfig_KeepsEnvReferencesUnresolved(t *testing.T) {
	t.Setenv("FERRO_TEST_SECRET", "super-secret-key")

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
strategy:
  mode: single
targets:
  - virtual_key: openai
mcp_servers:
  - name: database
    url: https://mcp-db.internal/mcp
    headers:
      Authorization: "Bearer ${FERRO_TEST_SECRET}"
plugins:
  - name: request-logger
    type: logging
    stage: before_request
    enabled: true
    config:
      api_key: "${FERRO_TEST_SECRET}"
      blocked_words: ["$100", "pa$$word"]
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	// The secret must NOT appear anywhere in the loaded Config.
	if got := cfg.Plugins[0].Config["api_key"]; got != "${FERRO_TEST_SECRET}" {
		t.Errorf("plugin api_key = %v; the Config must keep the ${VAR} reference, never the secret", got)
	}
	if got := cfg.MCPServers[0].Headers["Authorization"]; got != "Bearer ${FERRO_TEST_SECRET}" {
		t.Errorf("MCP header = %q; the Config must keep the ${VAR} reference, never the secret", got)
	}

	// And a literal '$' is data, preserved byte-for-byte.
	words, ok := cfg.Plugins[0].Config["blocked_words"].([]any)
	if !ok {
		t.Fatalf("blocked_words type %T", cfg.Plugins[0].Config["blocked_words"])
	}
	for i, want := range []string{"$100", "pa$$word"} {
		if got := words[i].(string); got != want {
			t.Errorf("blocked_words[%d] = %q, want %q", i, got, want)
		}
	}
}

// TestGateway_ResolvesPluginSecretsAtConstruction proves the other half: the plugin
// still receives the REAL value, resolved at Init, even though the Config never held it.
func TestGateway_ResolvesPluginSecretsAtConstruction(t *testing.T) {
	t.Setenv("FERRO_TEST_PLUGIN_SECRET", "resolved-at-use")

	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "openai"}},
		Plugins: []PluginConfig{{
			Name:    "envref-probe",
			Type:    "logging",
			Stage:   "before_request",
			Enabled: true,
			Config:  map[string]any{"token": "${FERRO_TEST_PLUGIN_SECRET}"}, //nolint:gosec // G101: an unresolved ${VAR} reference is the assertion, not a credential
		}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = gw.Close() }()
	if err := gw.LoadPlugins(); err != nil {
		t.Fatalf("LoadPlugins: %v", err)
	}

	got := <-envrefProbeConfig
	if got["token"] != "resolved-at-use" {
		t.Errorf("plugin received token = %v, want the resolved secret", got["token"])
	}
	// The Config the gateway still holds must NOT contain the secret.
	live := gw.GetConfig()
	if v := live.Plugins[0].Config["token"]; v != "${FERRO_TEST_PLUGIN_SECRET}" {
		t.Errorf("gateway Config token = %v; it must still hold the reference, not the secret", v)
	}
	if strings.Contains(live.Plugins[0].Config["token"].(string), "resolved-at-use") {
		t.Error("the materialised secret leaked into the gateway Config")
	}
}
