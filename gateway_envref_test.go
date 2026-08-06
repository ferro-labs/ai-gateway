package aigateway

import (
	"context"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
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

// TestGateway_ResolvesPluginSecretsAtConstruction proves the other half: the plugin
// still receives the REAL value, resolved at Init, even though the Config never held it.
func TestGateway_ResolvesPluginSecretsAtConstruction(t *testing.T) {
	t.Setenv("FERRO_TEST_PLUGIN_SECRET", "resolved-at-use")

	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "openai"}},
		Plugins: []config.PluginConfig{{
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
