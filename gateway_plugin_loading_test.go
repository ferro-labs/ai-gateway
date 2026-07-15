package aigateway

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/requestlog"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

func init() {
	plugin.RegisterFactory("test-plugin", func() plugin.Plugin {
		return &testPlugin{name: "test-plugin", typ: plugin.TypeGuardrail}
	})
	plugin.RegisterFactory("test-log-receiver", func() plugin.Plugin {
		p := &logReceiverPlugin{}
		lastLogReceiver = p
		return p
	})
	plugin.RegisterFactory("test-close-plugin", func() plugin.Plugin {
		closePluginMu.Lock()
		counter := closePluginCounter
		closePluginMu.Unlock()
		return &testPlugin{
			name: "test-close-plugin",
			typ:  plugin.TypeLogging,
			closeFn: func() error {
				if counter != nil {
					counter.Add(1)
				}
				return nil
			},
		}
	})
}

// lastLogReceiver holds the most recently constructed logReceiverPlugin. The
// receiver tests below run sequentially and build exactly one, so this is a
// safe way to reach the instance buildPluginManager created and injected.
var lastLogReceiver *logReceiverPlugin

var (
	closePluginMu      sync.Mutex
	closePluginCounter *atomic.Int32
)

// logReceiverPlugin records the writer the gateway injects, to prove
// buildPluginManager hands the shared request-log store to receiver plugins.
type logReceiverPlugin struct {
	injected    requestlog.Writer
	wasInjected bool
}

func (p *logReceiverPlugin) Name() string                                   { return "test-log-receiver" }
func (p *logReceiverPlugin) Type() plugin.PluginType                        { return plugin.TypeLogging }
func (p *logReceiverPlugin) Init(map[string]any) error                      { return nil }
func (p *logReceiverPlugin) Execute(context.Context, *plugin.Context) error { return nil }
func (p *logReceiverPlugin) Close() error                                   { return nil }
func (p *logReceiverPlugin) SetRequestLogWriter(w requestlog.Writer) {
	p.injected = w
	p.wasInjected = true
}

func loadReceiverPlugin(t *testing.T, gw *Gateway) *logReceiverPlugin {
	t.Helper()
	lastLogReceiver = nil
	if err := gw.LoadPlugins(); err != nil {
		t.Fatalf("LoadPlugins failed: %v", err)
	}
	if lastLogReceiver == nil {
		t.Fatal("receiver plugin was never constructed")
	}
	return lastLogReceiver
}

// The shared request-log store the gateway holds is injected into a receiver
// plugin as it loads.
func TestGateway_InjectsRequestLogWriterIntoPlugins(t *testing.T) {
	rec := &recordingLogWriter{}
	gw, _ := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
		Plugins: []PluginConfig{
			{Name: "test-log-receiver", Type: "logging", Stage: "after_request", Enabled: true},
		},
	})

	gw.SetRequestLogWriter(rec)

	p := loadReceiverPlugin(t, gw)
	if p.injected != requestlog.Writer(rec) {
		t.Fatalf("plugin injected writer = %v, want the store set via SetRequestLogWriter", p.injected)
	}
}

// Without a shared store, buildPluginManager does not call the receiver at all,
// so the plugin decides its own fallback rather than being handed nil.
func TestGateway_NoRequestLogWriter_PluginNotInjected(t *testing.T) {
	gw, _ := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
		Plugins: []PluginConfig{
			{Name: "test-log-receiver", Type: "logging", Stage: "after_request", Enabled: true},
		},
	})

	p := loadReceiverPlugin(t, gw)
	if p.wasInjected {
		t.Fatalf("SetRequestLogWriter was called with %v; want no injection when the gateway has no store", p.injected)
	}
}

type recordingLogWriter struct {
	entries []requestlog.Entry
}

func (w *recordingLogWriter) Write(_ context.Context, e requestlog.Entry) error {
	w.entries = append(w.entries, e)
	return nil
}

func TestGateway_LoadPlugins(t *testing.T) {
	gw, _ := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
		Plugins: []PluginConfig{
			{
				Name:    "test-plugin",
				Type:    "guardrail",
				Stage:   "before_request",
				Enabled: true,
				Config:  map[string]any{},
			},
		},
	})

	gw.RegisterProvider(&mockProvider{
		name:   mockProviderName,
		models: []string{"gpt-4o"},
		resp:   &providers.Response{ID: "ok"},
	})

	if err := gw.LoadPlugins(); err != nil {
		t.Fatalf("LoadPlugins failed: %v", err)
	}
	if !gw.plugins.HasPlugins() {
		t.Error("expected plugins to be registered")
	}
}

func TestGateway_LoadPlugins_UnknownPlugin(t *testing.T) {
	gw, _ := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
		Plugins: []PluginConfig{
			{
				Name:    "does-not-exist",
				Type:    "guardrail",
				Stage:   "before_request",
				Enabled: true,
				Config:  map[string]any{},
			},
		},
	})

	err := gw.LoadPlugins()
	if err == nil {
		t.Fatal("expected error for unknown plugin")
	}
	if got := err.Error(); got != "unknown plugin: does-not-exist" {
		t.Errorf("got error %q, want %q", got, "unknown plugin: does-not-exist")
	}
}

func TestGateway_ReloadConfig_ClosesOldPlugins(t *testing.T) {
	var oldClosed atomic.Int32
	closePluginMu.Lock()
	closePluginCounter = &oldClosed
	closePluginMu.Unlock()
	t.Cleanup(func() {
		closePluginMu.Lock()
		closePluginCounter = nil
		closePluginMu.Unlock()
	})

	gw, _ := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
		Plugins: []PluginConfig{
			{
				Name:    "test-close-plugin",
				Type:    "logging",
				Stage:   "after_request",
				Enabled: true,
				Config:  map[string]any{},
			},
		},
	})

	if err := gw.LoadPlugins(); err != nil {
		t.Fatalf("LoadPlugins failed: %v", err)
	}

	if err := gw.ReloadConfig(context.Background(), Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
	}); err != nil {
		t.Fatalf("ReloadConfig failed: %v", err)
	}
	if got := oldClosed.Load(); got != 1 {
		t.Fatalf("old plugin closes = %d, want 1", got)
	}
	if gw.plugins.HasPlugins() {
		t.Fatal("expected reload without plugin configs to clear registered plugins")
	}
}

func TestGateway_ReloadConfig_DefersOldPluginCloseUntilInFlightRouteFinishes(t *testing.T) {
	provider := newGateMockProvider(&providers.Response{ID: "ok", Model: "gpt-4o"}, nil)
	t.Cleanup(provider.releaseAll)
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: provider.Name()}},
	})

	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(provider)

	oldClosed := make(chan struct{})
	var closeOnce sync.Once
	afterRan := make(chan struct{})
	var afterOnce sync.Once
	if err := gw.RegisterPlugin(plugin.StageAfterRequest, &testPlugin{
		name: "in-flight-after",
		typ:  plugin.TypeLogging,
		execFn: func(context.Context, *plugin.Context) error {
			afterOnce.Do(func() { close(afterRan) })
			return nil
		},
		closeFn: func() error {
			closeOnce.Do(func() { close(oldClosed) })
			return nil
		},
	}); err != nil {
		t.Fatalf("RegisterPlugin: %v", err)
	}

	routeDone := make(chan error, 1)
	go func() {
		_, routeErr := gw.Route(context.Background(), providers.Request{
			Model:    "gpt-4o",
			Messages: []providers.Message{{Role: "user", Content: "hi"}},
		})
		routeDone <- routeErr
	}()

	provider.waitActive(t, 1)
	reloadDone := make(chan error, 1)
	go func() {
		reloadDone <- gw.ReloadConfig(context.Background(), Config{
			Strategy: StrategyConfig{Mode: ModeSingle},
			Targets:  []Target{{VirtualKey: provider.Name()}},
		})
	}()

	select {
	case err := <-reloadDone:
		if err != nil {
			t.Fatalf("ReloadConfig: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reload blocked behind in-flight route")
	}
	select {
	case <-oldClosed:
		t.Fatal("old plugin manager closed while an in-flight route could still run after/error plugins")
	default:
	}

	provider.releaseAll()

	select {
	case err := <-routeDone:
		if err != nil {
			t.Fatalf("Route: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for route")
	}
	select {
	case <-afterRan:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for after plugin")
	}
	select {
	case <-oldClosed:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for old plugin close")
	}
}

// ── mockEmbeddingProvider ─────────────────────────────────────────────────────
