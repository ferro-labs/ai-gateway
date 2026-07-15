package aigateway

import (
	"context"
	"testing"

	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

type testPlugin struct {
	name    string
	typ     plugin.PluginType
	execFn  func(ctx context.Context, pctx *plugin.Context) error
	closeFn func() error
}

func (p *testPlugin) Name() string              { return p.name }
func (p *testPlugin) Type() plugin.PluginType   { return p.typ }
func (p *testPlugin) Init(map[string]any) error { return nil }
func (p *testPlugin) Execute(ctx context.Context, pctx *plugin.Context) error {
	if p.execFn != nil {
		return p.execFn(ctx, pctx)
	}
	return nil
}
func (p *testPlugin) Close() error {
	if p.closeFn != nil {
		return p.closeFn()
	}
	return nil
}

func TestGateway_Route_WithBeforePlugin(t *testing.T) {
	gw, _ := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
	})

	gw.RegisterProvider(&mockProvider{
		name:   mockProviderName,
		models: []string{"gpt-4o"},
		resp:   &providers.Response{ID: "ok"},
	})

	called := false
	_ = gw.RegisterPlugin(plugin.StageBeforeRequest, &testPlugin{
		name: "tracker",
		typ:  plugin.TypeGuardrail,
		execFn: func(_ context.Context, _ *plugin.Context) error {
			called = true
			return nil
		},
	})

	_, err := gw.Route(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("before-request plugin was not called")
	}
}

func TestGateway_Route_PluginRejectsRequest(t *testing.T) {
	gw, _ := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
	})

	gw.RegisterProvider(&mockProvider{
		name:   mockProviderName,
		models: []string{"gpt-4o"},
		resp:   &providers.Response{ID: "should-not-reach"},
	})

	_ = gw.RegisterPlugin(plugin.StageBeforeRequest, &testPlugin{
		name: "blocker",
		typ:  plugin.TypeGuardrail,
		execFn: func(_ context.Context, pctx *plugin.Context) error {
			pctx.Reject = true
			pctx.Reason = "PII detected"
			return nil
		},
	})

	_, err := gw.Route(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected rejection error")
	}
}

// newMetricLabelGateway returns a gateway serving exactly "known-model".
