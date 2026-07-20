package aigateway

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/mcp"
	"github.com/ferro-labs/ai-gateway/plugin"
	providers "github.com/ferro-labs/ai-gateway/providers"
)

// callerTool is a tool the client declares and intends to execute itself.
func callerTool() providers.Tool {
	return providers.Tool{
		Type: "function",
		Function: providers.Function{
			Name:        "client_side_lookup",
			Description: "Executed by the caller, not the gateway.",
		},
	}
}

// The gateway must not advertise MCP tools on a request whose caller supplied
// its own tools array.
//
// Injecting both manufactures a turn neither party can resolve: the gateway
// cannot answer the caller's calls, and the caller cannot answer a tool it never
// declared and has no implementation for. Before this guard the gateway
// advertised mcp tools unconditionally, the model could call one alongside a
// caller tool, and the executor's ownership gate then declined the whole turn —
// returning a 200 carrying a tool_call_id the client could not act on, with no
// error surfaced anywhere.
func TestRoute_CallerSuppliedToolsSuppressMCPInjection(t *testing.T) {
	mcpSrv := newMCPTestServer(t)
	defer mcpSrv.Close()

	var (
		mu   sync.Mutex
		seen []providers.Tool
	)
	mp := &mockProvider{
		name:   "mock-tools",
		models: []string{"gpt-4o"},
		completeFn: func(_ context.Context, req providers.Request) (*providers.Response, error) {
			mu.Lock()
			seen = append([]providers.Tool{}, req.Tools...)
			mu.Unlock()
			return &providers.Response{
				ID:    "r1",
				Model: "gpt-4o",
				Choices: []providers.Choice{{
					Message:      providers.Message{Role: "assistant", Content: "done"},
					FinishReason: "stop",
				}},
			}, nil
		},
	}

	gw, err := newTestGateway(t, Config{
		Strategy:   StrategyConfig{Mode: ModeSingle},
		Targets:    []Target{{VirtualKey: "mock-tools"}},
		MCPServers: []mcp.ServerConfig{{Name: "s1", URL: mcpSrv.URL, TimeoutSeconds: 5}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(mp)

	select {
	case <-gw.MCPInitDone():
	case <-time.After(10 * time.Second):
		t.Fatal("MCP init timeout")
	}

	if _, err := gw.Route(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Tools:    []providers.Tool{callerTool()},
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("Route: %v", err)
	}

	mu.Lock()
	got := seen
	mu.Unlock()

	if len(got) != 1 {
		t.Fatalf("provider saw %d tools, want only the caller's 1: %+v", len(got), got)
	}
	if got[0].Function.Name != "client_side_lookup" {
		t.Errorf("provider saw tool %q, want the caller's client_side_lookup", got[0].Function.Name)
	}
	for _, tool := range got {
		if tool.Function.Name == "get_answer" {
			t.Error("gateway injected an MCP tool alongside the caller's own tools; " +
				"the model can then emit a turn neither party can resolve")
		}
	}
}

// With no caller tools, MCP still participates exactly as before.
func TestRoute_NoCallerToolsStillInjectsMCP(t *testing.T) {
	mcpSrv := newMCPTestServer(t)
	defer mcpSrv.Close()

	var (
		mu   sync.Mutex
		seen []providers.Tool
	)
	mp := &mockProvider{
		name:   "mock-tools2",
		models: []string{"gpt-4o"},
		completeFn: func(_ context.Context, req providers.Request) (*providers.Response, error) {
			mu.Lock()
			seen = append([]providers.Tool{}, req.Tools...)
			mu.Unlock()
			return &providers.Response{
				ID:    "r1",
				Model: "gpt-4o",
				Choices: []providers.Choice{{
					Message:      providers.Message{Role: "assistant", Content: "done"},
					FinishReason: "stop",
				}},
			}, nil
		},
	}

	gw, err := newTestGateway(t, Config{
		Strategy:   StrategyConfig{Mode: ModeSingle},
		Targets:    []Target{{VirtualKey: "mock-tools2"}},
		MCPServers: []mcp.ServerConfig{{Name: "s1", URL: mcpSrv.URL, TimeoutSeconds: 5}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(mp)

	select {
	case <-gw.MCPInitDone():
	case <-time.After(10 * time.Second):
		t.Fatal("MCP init timeout")
	}

	if _, err := gw.Route(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("Route: %v", err)
	}

	mu.Lock()
	got := seen
	mu.Unlock()

	var found bool
	for _, tool := range got {
		if tool.Function.Name == "get_answer" {
			found = true
		}
	}
	if !found {
		t.Errorf("MCP tool was not injected for a caller that supplied none: %+v", got)
	}
}

// Suppressing injection must also restore streaming: MCP is not participating,
// so there is nothing to buffer the stream for.
func TestRouteStream_CallerSuppliedToolsKeepStreaming(t *testing.T) {
	mcpSrv := newMCPTestServer(t)
	defer mcpSrv.Close()

	const wantChunks = 3
	sp := &chunkStreamProvider{
		mockProvider: mockProvider{name: "mock-stream-tools", models: []string{"gpt-4o"}},
		chunks:       wantChunks,
	}

	gw, err := newTestGateway(t, Config{
		Strategy:   StrategyConfig{Mode: ModeSingle},
		Targets:    []Target{{VirtualKey: "mock-stream-tools"}},
		MCPServers: []mcp.ServerConfig{{Name: "s1", URL: mcpSrv.URL, TimeoutSeconds: 5}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(sp)

	select {
	case <-gw.MCPInitDone():
	case <-time.After(10 * time.Second):
		t.Fatal("MCP init timeout")
	}

	ch, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Stream:   true,
		Tools:    []providers.Tool{callerTool()},
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("RouteStream: %v", err)
	}

	var got int
	for chunk := range ch {
		if chunk.Error != nil {
			t.Fatalf("stream chunk error: %v", chunk.Error)
		}
		got++
	}
	if got != wantChunks {
		t.Fatalf("got %d chunks, want %d — MCP collapsed the stream for a caller "+
			"whose own tools mean MCP is not participating", got, wantChunks)
	}
}

// A before_request plugin that adds tools must not retroactively disable MCP.
//
// RouteStream decides whether to divert a streaming request into the agentic
// loop before plugins run; Route used to decide whether MCP participates after
// they run. A transform plugin adding tools in between produced the worst of
// both: the stream was already diverted and buffered, and then MCP was switched
// off, so the caller lost streaming and got no agentic loop for it. Both
// decisions now read the caller's original request.
//
// Chunk count cannot distinguish the two cases — an active MCP loop collapses
// the stream legitimately — so the assertion is on whether MCP actually
// participated, i.e. whether its tools reached the provider.
func TestRouteStream_PluginAddedToolsDoNotDisableMCP(t *testing.T) {
	mcpSrv := newMCPTestServer(t)
	defer mcpSrv.Close()

	var (
		mu   sync.Mutex
		seen []providers.Tool
	)
	mp := &mockProvider{
		name:   "mock-plugin-tools",
		models: []string{"gpt-4o"},
		completeFn: func(_ context.Context, req providers.Request) (*providers.Response, error) {
			mu.Lock()
			seen = append([]providers.Tool{}, req.Tools...)
			mu.Unlock()
			return &providers.Response{
				ID:    "r1",
				Model: "gpt-4o",
				Choices: []providers.Choice{{
					Message:      providers.Message{Role: "assistant", Content: "done"},
					FinishReason: "stop",
				}},
			}, nil
		},
	}

	// The MCP server must actually be registered: without it g.mcpRegistry is
	// nil, RouteStream's diversion gate can never fire, and the assertion below
	// would hold whether or not the fix is present.
	gw, err := newTestGateway(t, Config{
		Strategy:   StrategyConfig{Mode: ModeSingle},
		Targets:    []Target{{VirtualKey: "mock-plugin-tools"}},
		MCPServers: []mcp.ServerConfig{{Name: "s1", URL: mcpSrv.URL, TimeoutSeconds: 5}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(mp)

	select {
	case <-gw.MCPInitDone():
	case <-time.After(10 * time.Second):
		t.Fatal("MCP init timeout")
	}

	// Adds a tool the caller never sent, mid-flight.
	if err := gw.RegisterPlugin(plugin.StageBeforeRequest, &testPlugin{
		name: "tool-adder",
		typ:  plugin.TypeTransform,
		execFn: func(_ context.Context, pctx *plugin.Context) error {
			if pctx.Request != nil {
				pctx.Request.Tools = append(pctx.Request.Tools, callerTool())
			}
			return nil
		},
	}); err != nil {
		t.Fatalf("RegisterPlugin: %v", err)
	}

	ch, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Stream:   true,
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("RouteStream: %v", err)
	}
	for chunk := range ch {
		if chunk.Error != nil {
			t.Fatalf("stream chunk error: %v", chunk.Error)
		}
	}

	mu.Lock()
	got := seen
	mu.Unlock()

	var sawMCPTool, sawPluginTool bool
	for _, tool := range got {
		switch tool.Function.Name {
		case "get_answer":
			sawMCPTool = true
		case "client_side_lookup":
			sawPluginTool = true
		}
	}
	if !sawPluginTool {
		t.Fatalf("the plugin's tool never reached the provider (%+v); the probe is broken, "+
			"so the assertion below would pass vacuously", got)
	}
	if !sawMCPTool {
		t.Errorf("MCP was disabled by a plugin-added tool after the stream had already been "+
			"diverted — the caller lost streaming and got no agentic loop for it: %+v", got)
	}
}
