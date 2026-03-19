package aigateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/internal/events"
	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/ferro-labs/ai-gateway/internal/metrics"
	"github.com/ferro-labs/ai-gateway/mcp"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	// Register built-in plugins so benchmark helpers can load them via LoadPlugins.
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/maxtoken"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/wordfilter"
)

// mockProvider is a test double for providers.Provider.
type mockProvider struct {
	name   string
	models []string
	resp   *providers.Response
	err    error
}

func (m *mockProvider) Name() string                  { return m.name }
func (m *mockProvider) SupportedModels() []string     { return m.models }
func (m *mockProvider) Models() []providers.ModelInfo { return nil }
func (m *mockProvider) SupportsModel(model string) bool {
	for _, mm := range m.models {
		if mm == model {
			return true
		}
	}
	return false
}
func (m *mockProvider) Complete(_ context.Context, _ providers.Request) (*providers.Response, error) {
	return m.resp, m.err
}

type mockStreamProvider struct {
	mockProvider
	streamErr error
}

func (m *mockStreamProvider) CompleteStream(_ context.Context, _ providers.Request) (<-chan providers.StreamChunk, error) {
	return nil, m.streamErr
}

func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := c.Write(m); err != nil {
		t.Fatalf("failed to read counter value: %v", err)
	}
	return m.GetCounter().GetValue()
}

func TestGateway_Route_Single(t *testing.T) {
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock"}},
	})
	gw.RegisterProvider(&mockProvider{
		name:   "mock",
		models: []string{"gpt-4o"},
		resp:   &providers.Response{ID: "r1", Model: "gpt-4o"},
	})

	resp, err := gw.Route(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ID != "r1" {
		t.Errorf("got ID %q, want r1", resp.ID)
	}
}

func TestGateway_Route_Fallback(t *testing.T) {
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeFallback},
		Targets: []Target{
			{VirtualKey: "bad"},
			{VirtualKey: "good"},
		},
	})
	gw.RegisterProvider(&mockProvider{
		name:   "bad",
		models: []string{"gpt-4o"},
		err:    fmt.Errorf("provider down"),
	})
	gw.RegisterProvider(&mockProvider{
		name:   "good",
		models: []string{"gpt-4o"},
		resp:   &providers.Response{ID: "fallback-ok"},
	})

	resp, err := gw.Route(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ID != "fallback-ok" {
		t.Errorf("got ID %q, want fallback-ok", resp.ID)
	}
}

func TestGateway_Route_NoTargets(t *testing.T) {
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
	})

	_, err := gw.Route(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for no targets")
	}
}

func TestGateway_RouteStream_ImmediateFailure_IncrementsProviderErrors(t *testing.T) {
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock-stream"}},
	})
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{
			name:   "mock-stream",
			models: []string{"gpt-4o"},
		},
		streamErr: errors.New("stream failed"),
	})

	beforeReq := counterValue(t, metrics.RequestsTotal.WithLabelValues("mock-stream", "gpt-4o", "error"))
	beforeProvErr := counterValue(t, metrics.ProviderErrors.WithLabelValues("mock-stream", "provider_error"))

	_, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected stream startup error")
	}

	afterReq := counterValue(t, metrics.RequestsTotal.WithLabelValues("mock-stream", "gpt-4o", "error"))
	afterProvErr := counterValue(t, metrics.ProviderErrors.WithLabelValues("mock-stream", "provider_error"))
	if afterReq-beforeReq != 1 {
		t.Fatalf("gateway_requests_total error delta = %v, want 1", afterReq-beforeReq)
	}
	if afterProvErr-beforeProvErr != 1 {
		t.Fatalf("gateway_provider_errors_total provider_error delta = %v, want 1", afterProvErr-beforeProvErr)
	}
}

func TestGateway_RouteStream_ImmediateCircuitOpen_IncrementsCircuitOpenProviderErrors(t *testing.T) {
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock-stream"}},
	})
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{
			name:   "mock-stream",
			models: []string{"gpt-4o"},
		},
		streamErr: circuitbreaker.ErrCircuitOpen,
	})

	beforeReq := counterValue(t, metrics.RequestsTotal.WithLabelValues("mock-stream", "gpt-4o", "error"))
	beforeProvErr := counterValue(t, metrics.ProviderErrors.WithLabelValues("mock-stream", "circuit_open"))

	_, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected circuit-open stream startup error")
	}

	afterReq := counterValue(t, metrics.RequestsTotal.WithLabelValues("mock-stream", "gpt-4o", "error"))
	afterProvErr := counterValue(t, metrics.ProviderErrors.WithLabelValues("mock-stream", "circuit_open"))
	if afterReq-beforeReq != 1 {
		t.Fatalf("gateway_requests_total error delta = %v, want 1", afterReq-beforeReq)
	}
	if afterProvErr-beforeProvErr != 1 {
		t.Fatalf("gateway_provider_errors_total circuit_open delta = %v, want 1", afterProvErr-beforeProvErr)
	}
}

func TestGateway_RouteStream_BeforePluginCanSetNilRequest(t *testing.T) {
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "missing"}},
	})

	_ = gw.RegisterPlugin(plugin.StageBeforeRequest, &testPlugin{
		name: "nil-request",
		typ:  plugin.TypeGuardrail,
		execFn: func(_ context.Context, pctx *plugin.Context) error {
			pctx.Request = nil
			return nil
		},
	})

	_, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for missing streaming provider")
	}
}

func TestGatewayClose_IsIdempotent(t *testing.T) {
	gw, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestGateway_Route_ProviderNotFound(t *testing.T) {
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "missing"}},
	})

	_, err := gw.Route(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for missing provider")
	}
}

func TestGateway_Route_HookPanicIsRecovered(t *testing.T) {
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock"}},
	})
	gw.RegisterProvider(&mockProvider{
		name:   "mock",
		models: []string{"gpt-4o"},
		resp:   &providers.Response{ID: "ok", Model: "gpt-4o"},
	})

	hookCalled := make(chan struct{}, 1)
	gw.AddHook(func(context.Context, string, map[string]interface{}) {
		hookCalled <- struct{}{}
		panic("boom")
	})

	_, err := gw.Route(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected route error: %v", err)
	}

	select {
	case <-hookCalled:
	case <-time.After(time.Second):
		t.Fatal("hook was not called")
	}
}

func TestGateway_PublishEvent_CallsAllHooks(t *testing.T) {
	gw, _ := New(Config{})

	called := make(chan string, 2)
	gw.AddHook(func(context.Context, string, map[string]interface{}) {
		called <- "first"
	})
	gw.AddHook(func(context.Context, string, map[string]interface{}) {
		called <- "second"
	})

	gw.publishEvent(context.Background(), events.CompletedRequest(
		"trace-123",
		"mock",
		"gpt-4o",
		time.Millisecond,
		false,
		1,
		1,
		models.CostResult{},
		true,
	))

	got := map[string]bool{}
	timeout := time.After(time.Second)
	for len(got) < 2 {
		select {
		case name := <-called:
			got[name] = true
		case <-timeout:
			t.Fatalf("timed out waiting for hooks, got %v", got)
		}
	}
}

func TestGateway_PublishEvent_EnqueuesAllHooksAsOneDispatch(t *testing.T) {
	gw := &Gateway{
		hookDispatchQ: make(chan hookDispatch, 2),
	}

	gw.hookSnapshot.Store([]EventHookFunc{
		func(context.Context, string, map[string]interface{}) {},
		func(context.Context, string, map[string]interface{}) {},
	})

	gw.publishEvent(context.Background(), events.CompletedRequest(
		"trace-123",
		"mock",
		"gpt-4o",
		time.Millisecond,
		false,
		1,
		1,
		models.CostResult{},
		true,
	))

	if got := len(gw.hookDispatchQ); got != 1 {
		t.Fatalf("queued hook dispatches = %d, want 1 (all hooks in one dispatch)", got)
	}
}

func TestGateway_PublishEvent_IncrementsDropMetricWhenQueueFull(t *testing.T) {
	counter := metrics.HookEventsDroppedTotal.WithLabelValues(SubjectRequestCompleted)
	before := counterValue(t, counter)

	gw := &Gateway{
		hookDispatchQ: make(chan hookDispatch, 1),
	}
	gw.hookSnapshot.Store([]EventHookFunc{
		func(context.Context, string, map[string]interface{}) {},
	})

	// Fill the queue.
	gw.publishEvent(context.Background(), events.CompletedRequest(
		"trace-fill", "mock", "gpt-4o", time.Millisecond, false, 1, 1, models.CostResult{}, true,
	))
	// This one should be dropped.
	gw.publishEvent(context.Background(), events.CompletedRequest(
		"trace-drop", "mock", "gpt-4o", time.Millisecond, false, 1, 1, models.CostResult{}, true,
	))

	after := counterValue(t, counter)
	if delta := after - before; delta != 1 {
		t.Fatalf("dropped hook metric delta = %v, want 1", delta)
	}
}

// testPlugin is a mock plugin for gateway tests.
type testPlugin struct {
	name   string
	typ    plugin.PluginType
	execFn func(ctx context.Context, pctx *plugin.Context) error
}

func (p *testPlugin) Name() string                      { return p.name }
func (p *testPlugin) Type() plugin.PluginType           { return p.typ }
func (p *testPlugin) Init(map[string]interface{}) error { return nil }
func (p *testPlugin) Execute(ctx context.Context, pctx *plugin.Context) error {
	if p.execFn != nil {
		return p.execFn(ctx, pctx)
	}
	return nil
}

func TestGateway_Route_WithBeforePlugin(t *testing.T) {
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock"}},
	})
	gw.RegisterProvider(&mockProvider{
		name:   "mock",
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
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock"}},
	})
	gw.RegisterProvider(&mockProvider{
		name:   "mock",
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

func init() {
	plugin.RegisterFactory("test-plugin", func() plugin.Plugin {
		return &testPlugin{name: "test-plugin", typ: plugin.TypeGuardrail}
	})
}

func TestGateway_LoadPlugins(t *testing.T) {
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock"}},
		Plugins: []PluginConfig{
			{
				Name:    "test-plugin",
				Type:    "guardrail",
				Stage:   "before_request",
				Enabled: true,
				Config:  map[string]interface{}{},
			},
		},
	})
	gw.RegisterProvider(&mockProvider{
		name:   "mock",
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
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock"}},
		Plugins: []PluginConfig{
			{
				Name:    "does-not-exist",
				Type:    "guardrail",
				Stage:   "before_request",
				Enabled: true,
				Config:  map[string]interface{}{},
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

// ── mockEmbeddingProvider ─────────────────────────────────────────────────────

type mockEmbeddingProvider struct {
	mockProvider
	capturedModel string
}

func (m *mockEmbeddingProvider) Embed(_ context.Context, req providers.EmbeddingRequest) (*providers.EmbeddingResponse, error) {
	m.capturedModel = req.Model
	return &providers.EmbeddingResponse{Model: req.Model}, nil
}

// ── mockImageProvider ─────────────────────────────────────────────────────────

type mockImageProvider struct {
	mockProvider
	capturedModel string
}

func (m *mockImageProvider) GenerateImage(_ context.Context, req providers.ImageRequest) (*providers.ImageResponse, error) {
	m.capturedModel = req.Model
	return &providers.ImageResponse{}, nil
}

// ── alias resolution tests ────────────────────────────────────────────────────

func TestGateway_Embed_ResolvesAlias(t *testing.T) {
	ep := &mockEmbeddingProvider{
		mockProvider: mockProvider{
			name:   "mock",
			models: []string{"text-embedding-3-small"},
		},
	}
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock"}},
		Aliases:  map[string]string{"my-embed": "text-embedding-3-small"},
	})
	gw.RegisterProvider(ep)

	_, err := gw.Embed(context.Background(), providers.EmbeddingRequest{
		Model: "my-embed",
		Input: "hello",
	})
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
	if ep.capturedModel != "text-embedding-3-small" {
		t.Errorf("provider received model %q, want text-embedding-3-small (alias not resolved)", ep.capturedModel)
	}
}

func TestGateway_Embed_NoAliasPassthrough(t *testing.T) {
	ep := &mockEmbeddingProvider{
		mockProvider: mockProvider{
			name:   "mock",
			models: []string{"text-embedding-3-small"},
		},
	}
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock"}},
	})
	gw.RegisterProvider(ep)

	_, err := gw.Embed(context.Background(), providers.EmbeddingRequest{
		Model: "text-embedding-3-small",
		Input: "hello",
	})
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
	if ep.capturedModel != "text-embedding-3-small" {
		t.Errorf("provider received model %q, want text-embedding-3-small", ep.capturedModel)
	}
}

func TestGateway_GenerateImage_ResolvesAlias(t *testing.T) {
	ip := &mockImageProvider{
		mockProvider: mockProvider{
			name:   "mock",
			models: []string{"dall-e-3"},
		},
	}
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock"}},
		Aliases:  map[string]string{"my-image-model": "dall-e-3"},
	})
	gw.RegisterProvider(ip)

	_, err := gw.GenerateImage(context.Background(), providers.ImageRequest{
		Model:  "my-image-model",
		Prompt: "a cat",
	})
	if err != nil {
		t.Fatalf("GenerateImage() error: %v", err)
	}
	if ip.capturedModel != "dall-e-3" {
		t.Errorf("provider received model %q, want dall-e-3 (alias not resolved)", ip.capturedModel)
	}
}

// ── StartDiscovery interval validation tests ──────────────────────────────────

func TestGateway_StartDiscovery_ZeroInterval(t *testing.T) {
	gw, _ := New(Config{})
	err := gw.StartDiscovery(context.Background(), 0)
	if err == nil {
		t.Fatal("StartDiscovery(0) should return an error")
	}
}

func TestGateway_StartDiscovery_NegativeInterval(t *testing.T) {
	gw, _ := New(Config{})
	err := gw.StartDiscovery(context.Background(), -time.Second)
	if err == nil {
		t.Fatal("StartDiscovery(-1s) should return an error")
	}
}

func TestGateway_StartDiscovery_ValidInterval(t *testing.T) {
	gw, _ := New(Config{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := gw.StartDiscovery(ctx, time.Hour)
	if err != nil {
		t.Fatalf("StartDiscovery(1h) returned unexpected error: %v", err)
	}
	// Cancel immediately; just verifies no panic and clean return.
	cancel()
}

// ─── MCP integration test ──────────────────────────────────────────────────

// multiCallProvider is a test provider that returns pre-configured responses
// in sequence, recording every request it receives for later inspection.
type multiCallProvider struct {
	name      string
	models    []string
	responses []*providers.Response
	mu        sync.Mutex
	requests  []providers.Request
}

func (m *multiCallProvider) Name() string                  { return m.name }
func (m *multiCallProvider) SupportedModels() []string     { return m.models }
func (m *multiCallProvider) Models() []providers.ModelInfo { return nil }
func (m *multiCallProvider) SupportsModel(model string) bool {
	for _, mm := range m.models {
		if mm == model {
			return true
		}
	}
	return false
}
func (m *multiCallProvider) Complete(_ context.Context, req providers.Request) (*providers.Response, error) {
	m.mu.Lock()
	idx := len(m.requests)
	m.requests = append(m.requests, req)
	m.mu.Unlock()
	if idx >= len(m.responses) {
		return nil, fmt.Errorf("multiCallProvider: no response configured for call %d", idx+1)
	}
	return m.responses[idx], nil
}
func (m *multiCallProvider) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

// newMCPTestServer returns a minimal httptest MCP server that exposes a
// single "get_answer" tool returning {"type":"text","text":"42"}.
func newMCPTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		defer r.Body.Close() //nolint:errcheck

		var rpcReq struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      any             `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&rpcReq); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		type rpcResp struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      any             `json:"id"`
			Result  json.RawMessage `json:"result"`
		}
		write := func(result any) {
			b, _ := json.Marshal(result)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(rpcResp{JSONRPC: "2.0", ID: rpcReq.ID, Result: b})
		}

		switch rpcReq.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "test-session-001")
			write(map[string]any{
				"name":         "test-mcp",
				"version":      "1.0",
				"capabilities": map[string]any{"tools": map[string]any{}},
			})
		case "tools/list":
			write(map[string]any{
				"tools": []map[string]any{{
					"name":        "get_answer",
					"description": "Returns the ultimate answer.",
					"inputSchema": json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`),
				}},
			})
		case "tools/call":
			write(map[string]any{
				"content": []map[string]any{{"type": "text", "text": "42"}},
				"isError": false,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// TestGateway_Route_MCPToolInjectionAndLoop verifies the full MCP agentic loop:
//  1. MCP tools are injected into the first LLM request.
//  2. When the LLM returns tool_calls the gateway calls the MCP server and
//     appends a tool-result message before re-routing.
//  3. The loop terminates when the LLM returns a normal response.
func TestGateway_Route_MCPToolInjectionAndLoop(t *testing.T) {
	// Start a mock MCP server.
	mcpSrv := newMCPTestServer(t)
	defer mcpSrv.Close()

	// Provider call 1 — returns a tool_call for "get_answer".
	// Provider call 2 — returns the final answer after seeing the tool result.
	mp := &multiCallProvider{
		name:   "test-provider",
		models: []string{"test-model"},
		responses: []*providers.Response{
			{
				ID:    "resp-1",
				Model: "test-model",
				Choices: []providers.Choice{{
					Message: providers.Message{
						Role: "assistant",
						ToolCalls: []providers.ToolCall{{
							ID:   "tc-1",
							Type: "function",
							Function: providers.FunctionCall{
								Name:      "get_answer",
								Arguments: `{"q":"what is the answer?"}`,
							},
						}},
					},
					FinishReason: "tool_calls",
				}},
			},
			{
				ID:    "resp-2",
				Model: "test-model",
				Choices: []providers.Choice{{
					Message: providers.Message{
						Role:    "assistant",
						Content: "The answer is 42.",
					},
					FinishReason: "stop",
				}},
			},
		},
	}

	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "test-provider"}},
		MCPServers: []mcp.ServerConfig{{
			Name:           "test-mcp",
			URL:            mcpSrv.URL + "/mcp",
			TimeoutSeconds: 5,
		}},
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	gw.RegisterProvider(mp)

	// Wait for MCP init (tools/list handshake) to finish.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	select {
	case <-gw.MCPInitDone():
	case <-ctx.Done():
		t.Fatal("timed out waiting for MCP initialization")
	}

	// Route the request through the agentic loop.
	resp, err := gw.Route(ctx, providers.Request{
		Model:    "test-model",
		Messages: []providers.Message{{Role: "user", Content: "What is the answer?"}},
	})
	if err != nil {
		t.Fatalf("Route() error: %v", err)
	}

	// The final response must be from the second provider call.
	if resp.ID != "resp-2" {
		t.Errorf("final response ID = %q, want resp-2", resp.ID)
	}

	// Provider must have been called exactly twice.
	if got := mp.callCount(); got != 2 {
		t.Errorf("provider called %d times, want 2", got)
	}

	mp.mu.Lock()
	requests := mp.requests
	mp.mu.Unlock()

	// First request must contain the "get_answer" tool injected by the gateway.
	var toolFound bool
	for _, tool := range requests[0].Tools {
		if tool.Function.Name == "get_answer" {
			toolFound = true
			break
		}
	}
	if !toolFound {
		t.Error("first request: get_answer tool not injected")
	}

	// Second request must contain a tool-result message for tc-1 with content "42".
	var toolMsg *providers.Message
	for i := range requests[1].Messages {
		if requests[1].Messages[i].Role == "tool" && requests[1].Messages[i].ToolCallID == "tc-1" {
			toolMsg = &requests[1].Messages[i]
			break
		}
	}
	if toolMsg == nil {
		t.Fatal("second request: missing tool-result message with ToolCallID=tc-1")
	}
	if toolMsg.Content != "42" {
		t.Errorf("tool-result content = %q, want \"42\"", toolMsg.Content)
	}
}

// TestGateway_RouteStream_MCPRedirect verifies that when MCP servers are
// configured, RouteStream routes through Route (running the full agentic loop)
// and wraps the final non-streaming response into a single-chunk channel.
func TestGateway_RouteStream_MCPRedirect(t *testing.T) {
	mcpSrv := newMCPTestServer(t)
	defer mcpSrv.Close()

	// Provider call 1 — returns a tool_call for "get_answer".
	// Provider call 2 — returns the final text after seeing the tool result.
	mp := &multiCallProvider{
		name:   "mock-mcp-stream",
		models: []string{"gpt-4o"},
		responses: []*providers.Response{
			{
				ID:    "s1",
				Model: "gpt-4o",
				Choices: []providers.Choice{{
					Message: providers.Message{
						Role: "assistant",
						ToolCalls: []providers.ToolCall{{
							ID:   "tc-stream-1",
							Type: "function",
							Function: providers.FunctionCall{
								Name:      "get_answer",
								Arguments: `{"q":"test"}`,
							},
						}},
					},
				}},
			},
			{
				ID:    "s2",
				Model: "gpt-4o",
				Choices: []providers.Choice{{
					Message: providers.Message{
						Role:    "assistant",
						Content: "The answer is 42.",
					},
					FinishReason: "stop",
				}},
			},
		},
	}

	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock-mcp-stream"}},
		MCPServers: []mcp.ServerConfig{{
			Name:           "test-mcp-stream",
			URL:            mcpSrv.URL,
			TimeoutSeconds: 10,
		}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(mp)

	// Wait for MCP init to complete.
	select {
	case <-gw.MCPInitDone():
	case <-time.After(5 * time.Second):
		t.Fatal("MCP init timeout")
	}

	// RouteStream with stream=true — should redirect through Route.
	ch, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Stream:   true,
		Messages: []providers.Message{{Role: "user", Content: "What is the answer?"}},
	})
	if err != nil {
		t.Fatalf("RouteStream error: %v", err)
	}

	// Drain the channel.
	var chunks []providers.StreamChunk
	for chunk := range ch {
		if chunk.Error != nil {
			t.Fatalf("stream chunk error: %v", chunk.Error)
		}
		chunks = append(chunks, chunk)
	}

	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk (MCP redirect), got %d", len(chunks))
	}
	if len(chunks[0].Choices) == 0 {
		t.Fatal("chunk has no choices")
	}
	if chunks[0].Choices[0].Delta.Content != "The answer is 42." {
		t.Errorf("chunk content = %q, want %q", chunks[0].Choices[0].Delta.Content, "The answer is 42.")
	}

	// Both provider calls must have fired (tool injection + final answer).
	if mp.callCount() != 2 {
		t.Errorf("expected 2 provider calls (agentic loop), got %d", mp.callCount())
	}
}

// mockBenchStreamProvider is a streaming provider that immediately returns a
// closed, empty channel — used only by benchmarks to avoid blocking drains.
type mockBenchStreamProvider struct {
	mockProvider
}

func (m *mockBenchStreamProvider) CompleteStream(_ context.Context, _ providers.Request) (<-chan providers.StreamChunk, error) {
	ch := make(chan providers.StreamChunk)
	close(ch)
	return ch, nil
}

// ── Benchmarks ───────────────────────────────────────────────────────────────

// silenceLogs redirects the gateway's package logger to io.Discard for the
// duration of a benchmark, preventing JSON log lines from corrupting the
// benchmark output (the gateway logger writes to os.Stdout by design).
func silenceLogs(b *testing.B) {
	b.Helper()
	prev := logging.Logger
	logging.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	b.Cleanup(func() { logging.Logger = prev })
}

// BenchmarkRoute measures the overhead of a single Route() call through a
// single-provider configuration with no plugins.
func BenchmarkRoute(b *testing.B) {
	silenceLogs(b)
	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "bench"}},
	})
	if err != nil {
		b.Fatal(err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   "bench",
		models: []string{"gpt-4o"},
		resp: &providers.Response{
			Choices: []providers.Choice{{Message: providers.Message{Role: "assistant", Content: "ok"}}},
		},
	})

	req := providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hello"}},
	}
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := gw.Route(ctx, req); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRouteStream measures the overhead of a RouteStream() call (no MCP,
// no plugins). The benchmark drains the channel to completion each iteration.
func BenchmarkRouteStream(b *testing.B) {
	silenceLogs(b)

	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "bench-stream"}},
	})
	if err != nil {
		b.Fatal(err)
	}
	gw.RegisterProvider(&mockBenchStreamProvider{
		mockProvider: mockProvider{
			name:   "bench-stream",
			models: []string{"gpt-4o"},
		},
	})

	req := providers.Request{
		Model:    "gpt-4o",
		Stream:   true,
		Messages: []providers.Message{{Role: "user", Content: "hello"}},
	}
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := gw.RouteStream(ctx, req)
		if err != nil {
			b.Fatal(err)
		}
		for range out { //nolint:revive
		}
	}
}

// BenchmarkRoute_WithPlugins measures Route() with a two-plugin before_request
// chain (word-filter + max-token) loaded via LoadPlugins.
func BenchmarkRoute_WithPlugins(b *testing.B) {
	silenceLogs(b)
	cfg := Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "bench-plugins"}},
		Plugins: []PluginConfig{
			{Name: "word-filter", Enabled: true, Stage: "before_request", Config: map[string]interface{}{"blocked_words": []interface{}{}}},
			{Name: "max-token", Enabled: true, Stage: "before_request", Config: map[string]interface{}{"max_input_tokens": 1000}},
		},
	}
	gw, err := New(cfg)
	if err != nil {
		b.Fatal(err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   "bench-plugins",
		models: []string{"gpt-4o"},
		resp: &providers.Response{
			Choices: []providers.Choice{{Message: providers.Message{Role: "assistant", Content: "ok"}}},
		},
	})
	if err := gw.LoadPlugins(); err != nil {
		b.Fatal(err)
	}

	req := providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hello world"}},
	}
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := gw.Route(ctx, req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRoute_WithHook(b *testing.B) {
	silenceLogs(b)
	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "bench-hook"}},
	})
	if err != nil {
		b.Fatal(err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   "bench-hook",
		models: []string{"gpt-4o"},
		resp: &providers.Response{
			Choices: []providers.Choice{{Message: providers.Message{Role: "assistant", Content: "ok"}}},
		},
	})

	var calls atomic.Int64
	gw.AddHook(func(context.Context, string, map[string]interface{}) {
		calls.Add(1)
	})

	req := providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hello"}},
	}
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := gw.Route(ctx, req); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()

	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() < int64(b.N) {
		if time.Now().After(deadline) {
			b.Fatalf("timed out waiting for hook dispatch: completed=%d want=%d", calls.Load(), b.N)
		}
		time.Sleep(time.Millisecond)
	}
}

// BenchmarkFindByModel measures repeated model lookup after the gateway has
// built its lookup indexes and per-model cache.
func BenchmarkFindByModel(b *testing.B) {
	silenceLogs(b)
	gw, err := New(Config{})
	if err != nil {
		b.Fatal(err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   "bench-find",
		models: []string{"gpt-4o"},
		resp: &providers.Response{
			Choices: []providers.Choice{{Message: providers.Message{Role: "assistant", Content: "ok"}}},
		},
	})

	if _, ok := gw.FindByModel("gpt-4o"); !ok {
		b.Fatal("expected model lookup to succeed")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := gw.FindByModel("gpt-4o"); !ok {
			b.Fatal("expected model lookup to succeed")
		}
	}
}

func BenchmarkPublishEvent(b *testing.B) {
	silenceLogs(b)
	gw, err := New(Config{})
	if err != nil {
		b.Fatal(err)
	}

	var calls atomic.Int64
	var wg sync.WaitGroup
	wg.Add(b.N)
	gw.AddHook(func(context.Context, string, map[string]interface{}) {
		calls.Add(1)
		wg.Done()
	})

	event := events.CompletedRequest(
		"trace-bench",
		"bench",
		"gpt-4o",
		time.Millisecond,
		false,
		1,
		1,
		models.CostResult{},
		true,
	)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		gw.publishEvent(ctx, event)
	}
	b.StopTimer()

	waitDone := make(chan struct{})
	go func() {
		defer close(waitDone)
		wg.Wait()
	}()

	select {
	case <-waitDone:
	case <-time.After(5 * time.Second):
		b.Fatalf("timed out waiting for hook dispatch: completed=%d want=%d", calls.Load(), b.N)
	}
}
