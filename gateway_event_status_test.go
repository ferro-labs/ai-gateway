package aigateway

import (
	"context"
	"net/http"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

// A denial is not a server error, on the wire or off the box.
//
// A rate-limit rejection answers the caller 429 and is counted as a rejection
// by the gateway's own metrics, while the event shipped to observability
// exporters said 500 — so an exporter's error rate disagreed with both, and a
// throttled client looked like a broken gateway to whoever was watching.
func TestGateway_FailedEventCarriesTheCallerStatus(t *testing.T) {
	tests := []struct {
		name       string
		pluginType plugin.PluginType
		want       int
	}{
		{"rate limit denial", plugin.TypeRateLimit, http.StatusTooManyRequests},
		{"guardrail denial", plugin.TypeGuardrail, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gw, _ := newTestGateway(t, config.Config{
				Strategy: config.StrategyConfig{Mode: config.ModeSingle},
				Targets:  []config.Target{{VirtualKey: mockProviderName}},
			})

			ep := &eventCapturingProvider{recordingActive: true}
			gw.SetObservability(ep)

			gw.RegisterProvider(&mockEmbeddingProvider{
				mockProvider: mockProvider{name: mockProviderName, models: []string{testModel}},
			})
			_ = gw.RegisterPlugin(plugin.StageBeforeRequest, &testPlugin{
				name: "denier",
				typ:  tt.pluginType,
				execFn: func(_ context.Context, pctx *plugin.Context) error {
					pctx.Reject = true
					pctx.Reason = "over the limit"
					return nil
				},
			})

			if _, err := gw.Embed(context.Background(), providers.EmbeddingRequest{
				Model: testModel,
				Input: "hi",
			}); err == nil {
				t.Fatal("expected the plugin denial to abort the request")
			}

			evts := ep.capturedEvents()
			if len(evts) != 1 {
				t.Fatalf("expected 1 event, got %d: %v", len(evts), evts)
			}
			if got := evts[0].Status; got != tt.want {
				t.Errorf("exporter event Status = %d, want %d — the status the caller was given", got, tt.want)
			}
		})
	}
}
