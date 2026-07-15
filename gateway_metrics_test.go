package aigateway

import (
	"context"
	"errors"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/metrics"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

func newMetricLabelGateway(t *testing.T) *Gateway {
	t.Helper()
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
	})

	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   mockProviderName,
		models: []string{"known-model"},
		resp:   &providers.Response{ID: "should-not-reach"},
	})
	return gw
}

func TestGateway_MetricModel(t *testing.T) {
	gw := newMetricLabelGateway(t)

	tests := []struct {
		name  string
		model string
		want  string
	}{
		{name: "known model passes through", model: "known-model", want: "known-model"},
		{name: "empty model buckets", model: "", want: metrics.UnknownModelLabel},
		{name: "arbitrary model buckets", model: "totally-made-up-model", want: metrics.UnknownModelLabel},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := gw.metricModel(tt.model); got != tt.want {
				t.Fatalf("metricModel(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

// A plugin rejection carries the raw client model to the "rejected" counter.
func TestGateway_Route_PluginRejectsUnknownModelBucketsMetricLabel(t *testing.T) {
	const rawModel = "user-supplied-high-cardinality-rejected"
	if requestMetricLabelExists(t, "", rawModel, "rejected") {
		t.Fatalf("raw model label %q already exists before test", rawModel)
	}

	gw := newMetricLabelGateway(t)
	_ = gw.RegisterPlugin(plugin.StageBeforeRequest, &testPlugin{
		name: "blocker",
		typ:  plugin.TypeGuardrail,
		execFn: func(_ context.Context, pctx *plugin.Context) error {
			pctx.Reject = true
			pctx.Reason = "blocked"
			return nil
		},
	})

	unknownCounter := metrics.ForRequest("", metrics.UnknownModelLabel).Rejected
	before := counterValue(t, unknownCounter)
	_, err := gw.Route(context.Background(), providers.Request{
		Model:    rawModel,
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected rejection error")
	}
	if delta := counterValue(t, unknownCounter) - before; delta != 1 {
		t.Fatalf("unknown rejected counter delta = %v, want 1", delta)
	}
	if requestMetricLabelExists(t, "", rawModel, "rejected") {
		t.Fatalf("raw rejected model label %q should not be created", rawModel)
	}
}

// The error path needs no plugin at all: any unroutable model reaches it, which
// makes it the cardinality vector a client can trigger against a stock gateway.
func TestGateway_Route_UnroutableModelBucketsErrorMetricLabel(t *testing.T) {
	const rawModel = "user-supplied-high-cardinality-error"
	if requestMetricLabelExists(t, "", rawModel, "error") {
		t.Fatalf("raw model label %q already exists before test", rawModel)
	}

	gw := newMetricLabelGateway(t)
	unknownCounter := metrics.ForRequest("", metrics.UnknownModelLabel).Error
	before := counterValue(t, unknownCounter)

	_, err := gw.Route(context.Background(), providers.Request{
		Model:    rawModel,
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected routing error for unsupported model")
	}
	if delta := counterValue(t, unknownCounter) - before; delta != 1 {
		t.Fatalf("unknown error counter delta = %v, want 1", delta)
	}
	if requestMetricLabelExists(t, "", rawModel, "error") {
		t.Fatalf("raw error model label %q should not be created", rawModel)
	}
}

// wildcardStreamProvider mirrors the 14 providers that accept any model ID
// (openrouter, ollama, azure_openai, …). They route a raw client model all the
// way to a provider call, so a CompleteStream failure lands on the error counter
// with that raw value.
type wildcardStreamProvider struct {
	mockStreamProvider
}

func (p *wildcardStreamProvider) SupportsModel(string) bool { return true }

// A wildcard provider can accept an arbitrary model and stream successfully, so
// the success/token/duration labels emitted by streamwrap must be bounded too —
// not just the error label. The raw model still reaches cost lookup and events.
func TestGateway_RouteStream_SuccessBucketsMetricLabel(t *testing.T) {
	const rawModel = "user-supplied-high-cardinality-stream-success"
	if requestMetricLabelExists(t, mockProviderName, rawModel, "success") {
		t.Fatalf("raw model label %q already exists before test", rawModel)
	}

	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
	})

	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}
	gw.RegisterProvider(&wildcardStreamProvider{mockStreamProvider: mockStreamProvider{
		mockProvider: mockProvider{name: mockProviderName, models: []string{"known-model"}},
	}})

	unknownCounter := metrics.ForRequest(mockProviderName, metrics.UnknownModelLabel).Success
	before := counterValue(t, unknownCounter)

	ch, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    rawModel,
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("RouteStream: %v", err)
	}
	drainStream(t, ch) // streamwrap emits metrics before closing the channel

	if delta := counterValue(t, unknownCounter) - before; delta != 1 {
		t.Fatalf("unknown success counter delta = %v, want 1", delta)
	}
	if requestMetricLabelExists(t, mockProviderName, rawModel, "success") {
		t.Fatalf("raw success model label %q should not be created", rawModel)
	}
}

func TestGateway_RouteStream_WildcardProviderBucketsErrorMetricLabel(t *testing.T) {
	const rawModel = "user-supplied-high-cardinality-stream"
	if requestMetricLabelExists(t, mockProviderName, rawModel, "error") {
		t.Fatalf("raw model label %q already exists before test", rawModel)
	}

	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
	})

	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}
	gw.RegisterProvider(&wildcardStreamProvider{mockStreamProvider: mockStreamProvider{
		mockProvider: mockProvider{name: mockProviderName, models: []string{"known-model"}},
		streamErr:    errors.New("upstream rejected model"),
	}})

	unknownCounter := metrics.ForRequest(mockProviderName, metrics.UnknownModelLabel).Error
	before := counterValue(t, unknownCounter)

	if _, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    rawModel,
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	}); err == nil {
		t.Fatal("expected streaming error")
	}
	if delta := counterValue(t, unknownCounter) - before; delta != 1 {
		t.Fatalf("unknown stream error counter delta = %v, want 1", delta)
	}
	if requestMetricLabelExists(t, mockProviderName, rawModel, "error") {
		t.Fatalf("raw stream error model label %q should not be created", rawModel)
	}
}
