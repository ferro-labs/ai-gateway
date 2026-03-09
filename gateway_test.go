package aigateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/internal/metrics"
	"github.com/ferro-labs/ai-gateway/mcp"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
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
