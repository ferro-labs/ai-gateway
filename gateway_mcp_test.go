package aigateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/internal/authctx"
	"github.com/ferro-labs/ai-gateway/mcp"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
	"github.com/ferro-labs/ai-gateway/plugin"
	providers "github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// mcpUndefinedVar names the environment variable the malformed-server fixtures
// below reference. Their whole premise is that it does NOT resolve, so the
// fixtures must not depend on it merely happening to be absent from the ambient
// environment: an exported value would make the "broken" server register and
// invert what every one of these tests asserts.
const mcpUndefinedVar = "GATEWAY_MCP_TEST_UNDEFINED_VAR"

// requireUnsetEnv guarantees key is unset for the duration of the test and
// restores any prior value afterwards.
func requireUnsetEnv(t *testing.T, key string) {
	t.Helper()
	prev, had := os.LookupEnv(key)
	if !had {
		return
	}
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if err := os.Setenv(key, prev); err != nil {
			t.Errorf("restore %s: %v", key, err)
		}
	})
}

// TestWireMCPLocked_MalformedServerDoesNotBlockOthers verifies that one MCP
// server with an unresolvable ${VAR} in its headers does not prevent
// subsequent, well-formed servers in the same config from being registered.
func TestWireMCPLocked_MalformedServerDoesNotBlockOthers(t *testing.T) {
	requireUnsetEnv(t, mcpUndefinedVar)
	gw := &Gateway{log: logger.Default()}
	ctx, cancel := context.WithCancel(context.Background())
	gw.shutdownCtx = ctx
	gw.shutdownCancel = cancel
	t.Cleanup(cancel)

	cfg := config.Config{
		MCPServers: []mcp.ServerConfig{
			{
				Name: "broken",
				URL:  "http://127.0.0.1:1/mcp",
				Headers: map[string]string{
					"Authorization": "Bearer ${" + mcpUndefinedVar + "}",
				},
			},
			{
				Name: "good",
				URL:  "http://127.0.0.1:1/mcp",
			},
		},
	}

	gw.wireMCPLocked(cfg, "test: mcp init failed")

	if gw.mcpRegistry == nil {
		t.Fatal("mcpRegistry is nil, want a registry containing the well-formed server")
	}
	names := gw.mcpRegistry.ServerNames()
	if !slices.Contains(names, "good") {
		t.Errorf("ServerNames() = %v, want it to contain %q", names, "good")
	}

	// The broken server is recorded rather than dropped: readiness reads the
	// registry, so a server missing from it cannot be reported at all. It is
	// present and unready, with its reason retained.
	if !slices.Contains(names, "broken") {
		t.Fatalf("ServerNames() = %v, want it to contain %q so its failure stays visible", names, "broken")
	}
	if gw.mcpRegistry.IsReady("broken") {
		t.Error("a server whose headers never resolved is reported ready")
	}
	for _, st := range gw.mcpRegistry.Status() {
		if st.Name == "broken" && st.LastError == "" {
			t.Error("Status() lost the reason the server could not be built")
		}
	}
}

// TestReloadConfig_MalformedMCPServerDoesNotLeaveStaleRegistry verifies that a
// reload whose new config contains one malformed MCP server still rebuilds the
// registry from the new config, rather than leaving the pre-reload registry in
// place.
func TestReloadConfig_MalformedMCPServerDoesNotLeaveStaleRegistry(t *testing.T) {
	requireUnsetEnv(t, mcpUndefinedVar)
	gw := &Gateway{log: logger.Default()}
	ctx, cancel := context.WithCancel(context.Background())
	gw.shutdownCtx = ctx
	gw.shutdownCancel = cancel
	t.Cleanup(cancel)

	base := config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "test-provider"}},
	}

	initial := base
	initial.MCPServers = []mcp.ServerConfig{{Name: "old-only", URL: "http://127.0.0.1:1/mcp"}}
	gw.wireMCPLocked(initial, "test: initial mcp init failed")

	reloaded := base
	reloaded.MCPServers = []mcp.ServerConfig{
		{
			Name: "broken",
			URL:  "http://127.0.0.1:1/mcp",
			Headers: map[string]string{
				"Authorization": "Bearer ${" + mcpUndefinedVar + "}",
			},
		},
		{Name: "good2", URL: "http://127.0.0.1:1/mcp"},
	}

	if err := gw.ReloadConfig(context.Background(), reloaded); err != nil {
		t.Fatalf("ReloadConfig() error = %v", err)
	}

	gw.mu.RLock()
	reg := gw.mcpRegistry
	gw.mu.RUnlock()
	if reg == nil {
		t.Fatal("mcpRegistry is nil after reload, want the rebuilt registry")
	}
	names := reg.ServerNames()
	if slices.Contains(names, "old-only") {
		t.Errorf("ServerNames() = %v, still contains the pre-reload server; registry was not rebuilt from the new config", names)
	}
	if !slices.Contains(names, "good2") {
		t.Errorf("ServerNames() = %v, want it to contain %q from the reloaded config", names, "good2")
	}
}

// TestWireMCPLocked_MaxCallDepthIgnoresSkippedServers verifies that a server
// skipped due to unresolvable headers does not contribute its MaxCallDepth to
// the shared executor's depth limit.
func TestWireMCPLocked_MaxCallDepthIgnoresSkippedServers(t *testing.T) {
	requireUnsetEnv(t, mcpUndefinedVar)
	gw := &Gateway{log: logger.Default()}
	ctx, cancel := context.WithCancel(context.Background())
	gw.shutdownCtx = ctx
	gw.shutdownCancel = cancel
	t.Cleanup(cancel)

	// The good server must actually serve tools: ShouldContinueLoop only arms
	// for tool calls MCP owns, so a registry that discovered nothing can no
	// longer stand in for a healthy one.
	goodSrv := newMCPTestServer(t)
	defer goodSrv.Close()

	cfg := config.Config{
		MCPServers: []mcp.ServerConfig{
			{
				Name:         "broken",
				URL:          "http://127.0.0.1:1/mcp",
				MaxCallDepth: 1,
				Headers: map[string]string{
					"Authorization": "Bearer ${" + mcpUndefinedVar + "}",
				},
			},
			{
				Name:         "good",
				URL:          goodSrv.URL,
				MaxCallDepth: 5,
			},
		},
	}

	gw.wireMCPLocked(cfg, "test: mcp init failed")

	if gw.mcpExecutor == nil {
		t.Fatal("mcpExecutor is nil, want an executor built from the well-formed server")
	}

	select {
	case <-gw.MCPInitDone():
	case <-time.After(10 * time.Second):
		t.Fatal("MCP init timeout")
	}

	resp := &core.Response{
		Choices: []core.Choice{
			{Message: core.Message{ToolCalls: []core.ToolCall{{ID: "1", Function: core.FunctionCall{Name: "get_answer"}}}}},
		},
	}

	// The broken server's max_call_depth: 1 must not clamp the shared
	// executor's depth limit down from the good server's max_call_depth: 5.
	if !gw.mcpExecutor.ShouldContinueLoop(resp, 4) {
		t.Error("ShouldContinueLoop(resp, 4) = false, want true: depth limit should be 5 (the good server's), not 1 (the skipped server's)")
	}
}

// R8: closeOnce has already fired by the time a late reload lands, so a registry
// built after shutdown would spawn its subprocesses and leave nothing able to
// close them — leaked for the life of the host process.
func TestReloadConfigAfterCloseIsRejected(t *testing.T) {
	gw, err := New(config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "openai"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := gw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	err = gw.ReloadConfig(context.Background(), config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "openai"}},
		MCPServers: []mcp.ServerConfig{{
			Name: "late", URL: "http://127.0.0.1:1/mcp",
		}},
	})
	if err == nil {
		t.Fatal("ReloadConfig after Close succeeded; it would spawn MCP subprocesses nothing can ever close")
	}
}

// R5: ReloadConfig writes g.mcpRegistry under g.mu while Close used to read it
// after unlocking. Race-detector reproducible, and the losing interleaving left
// a live registry unclosed. Run this with -race.
func TestCloseAndReloadConfigDoNotRace(t *testing.T) {
	mcpSrv := newMCPTestServer(t)
	defer mcpSrv.Close()

	base := config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "openai"}},
		MCPServers: []mcp.ServerConfig{{
			Name: "s1", URL: mcpSrv.URL, TimeoutSeconds: 5,
		}},
	}

	gw, err := New(base)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	select {
	case <-gw.MCPInitDone():
	case <-time.After(10 * time.Second):
		t.Fatal("MCP init timeout")
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		// A late reload may legitimately be rejected once Close has won the
		// race; both outcomes are correct, neither may race or leak.
		_ = gw.ReloadConfig(context.Background(), base)
	}()
	go func() {
		defer wg.Done()
		_ = gw.Close()
	}()
	wg.Wait()
}

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

	gw, err := newTestGateway(t, config.Config{
		Strategy:   config.StrategyConfig{Mode: config.ModeSingle},
		Targets:    []config.Target{{VirtualKey: "mock-tools"}},
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

	gw, err := newTestGateway(t, config.Config{
		Strategy:   config.StrategyConfig{Mode: config.ModeSingle},
		Targets:    []config.Target{{VirtualKey: "mock-tools2"}},
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

	gw, err := newTestGateway(t, config.Config{
		Strategy:   config.StrategyConfig{Mode: config.ModeSingle},
		Targets:    []config.Target{{VirtualKey: "mock-stream-tools"}},
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
	gw, err := newTestGateway(t, config.Config{
		Strategy:   config.StrategyConfig{Mode: config.ModeSingle},
		Targets:    []config.Target{{VirtualKey: "mock-plugin-tools"}},
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

// mcpServerReturning is an MCP server whose single tool answers with text the
// test chooses, so a guardrail can be pointed at content the CALLER never sent.
func mcpServerReturning(t *testing.T, toolText string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close() //nolint:errcheck // test handler
		var rpc struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&rpc); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		write := func(result any) {
			b, _ := json.Marshal(result)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": rpc.ID, "result": json.RawMessage(b),
			})
		}
		switch rpc.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "guardrail-session")
			write(map[string]any{
				"name": "guard-mcp", "version": "1.0",
				"capabilities": map[string]any{"tools": map[string]any{}},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			write(map[string]any{"tools": []map[string]any{{
				"name": "lookup", "description": "Looks something up.",
				"inputSchema": json.RawMessage(`{"type":"object"}`),
			}}})
		case "tools/call":
			write(map[string]any{
				"content": []map[string]any{{"type": "text", "text": toolText}},
				"isError": false,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// A configured content guardrail must inspect every provider call, including
// the turns an agentic loop makes on the caller's behalf.
//
// Those turns carry tool RESULTS produced by an external MCP server — content
// the caller never wrote and the operator has the least reason to trust. The
// loop called the router directly, so the before stage had run once, against
// the prompt alone, and the guardrail never saw them. It is the same governance
// escape as an unguarded surface, one level in.
func TestRoute_MCPLoopRunsGuardrailsOnToolResults(t *testing.T) {
	const blocked = "hunter2"

	mcpSrv := mcpServerReturning(t, "the password is "+blocked)
	defer mcpSrv.Close()

	// Turn 1 asks for the tool; turn 2 would answer. Turn 2 must never be made:
	// the guardrail rejects the request carrying the tool result.
	provider := &multiCallProvider{
		name:   "guard-provider",
		models: []string{"guard-model"},
		responses: []*providers.Response{
			{
				ID: "r1", Model: "guard-model", Provider: "guard-provider",
				Choices: []providers.Choice{{
					Message: providers.Message{Role: "assistant", ToolCalls: []providers.ToolCall{{
						ID: "call-1", Type: "function",
						Function: providers.FunctionCall{Name: "lookup", Arguments: "{}"},
					}}},
					FinishReason: "tool_calls",
				}},
			},
			{
				ID: "r2", Model: "guard-model", Provider: "guard-provider",
				Choices: []providers.Choice{{
					Message:      providers.Message{Role: "assistant", Content: "done"},
					FinishReason: "stop",
				}},
			},
		},
	}

	gw, err := newTestGateway(t, config.Config{
		Strategy:   config.StrategyConfig{Mode: config.ModeSingle},
		Targets:    []config.Target{{VirtualKey: "guard-provider"}},
		MCPServers: []mcp.ServerConfig{{Name: "s1", URL: mcpSrv.URL, TimeoutSeconds: 5}},
		Plugins: []config.PluginConfig{{
			Name: "word-filter", Type: "guardrail", Stage: "before_request", Enabled: true,
			Config: map[string]any{"blocked_words": []any{blocked}},
		}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	gw.RegisterProvider(provider)
	if err := gw.LoadPlugins(); err != nil {
		t.Fatalf("load plugins: %v", err)
	}

	select {
	case <-gw.MCPInitDone():
	case <-time.After(10 * time.Second):
		t.Fatal("MCP init timeout")
	}
	if gw.mcpRegistry == nil || len(gw.mcpRegistry.AllTools()) == 0 {
		t.Fatal("no MCP tools registered; the loop would never run and this would pass vacuously")
	}

	_, routeErr := gw.Route(context.Background(), providers.Request{
		Model:    "guard-model",
		Messages: []providers.Message{{Role: "user", Content: "look it up"}},
	})

	if routeErr == nil {
		t.Fatal("Route succeeded; the guardrail never saw the tool result")
	}
	var rejection *plugin.RejectionError
	if !errors.As(routeErr, &rejection) {
		t.Fatalf("error = %T (%v), want a plugin rejection", routeErr, routeErr)
	}
	if !strings.Contains(strings.ToLower(rejection.Error()), "word") {
		t.Errorf("rejection = %q, want the word filter's", rejection.Error())
	}

	// The second turn must not have been made: the guardrail runs BEFORE the
	// call, so blocked content never reaches the provider.
	provider.mu.Lock()
	calls := len(provider.requests)
	provider.mu.Unlock()
	if calls != 1 {
		t.Errorf("provider was called %d times, want 1 — the blocked turn must not be sent", calls)
	}
}

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
func (m *multiCallProvider) ConfiguredModels() []string    { return m.models }
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
		defer r.Body.Close() //nolint:errcheck // test HTTP server handler; request body close error is irrelevant

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

	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "test-provider"}},
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
		t.Fatalf("provider called %d times, want 2", got)
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

	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "mock-mcp-stream"}},
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

// mcpUsageTurns are the per-turn usages the agentic loop below spends. Three
// turns, all different, so a total that happens to equal one turn cannot be the
// sum of the others.
var mcpUsageTurns = []providers.Usage{
	{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120, ReasoningTokens: 5, CacheReadTokens: 40, CacheWriteTokens: 10},
	{PromptTokens: 300, CompletionTokens: 30, TotalTokens: 330, ReasoningTokens: 7, CacheReadTokens: 0, CacheWriteTokens: 3},
	{PromptTokens: 700, CompletionTokens: 60, TotalTokens: 760, ReasoningTokens: 11, CacheReadTokens: 2, CacheWriteTokens: 0},
}

// mcpUsageTotal is what the whole request spent.
func mcpUsageTotal() providers.Usage {
	var total providers.Usage
	for _, u := range mcpUsageTurns {
		total.PromptTokens += u.PromptTokens
		total.CompletionTokens += u.CompletionTokens
		total.TotalTokens += u.TotalTokens
		total.ReasoningTokens += u.ReasoningTokens
		total.CacheReadTokens += u.CacheReadTokens
		total.CacheWriteTokens += u.CacheWriteTokens
	}
	return total
}

// mcpToolCallTurn is a response asking for one get_answer call, which is what
// keeps the agentic loop going for another turn.
func mcpToolCallTurn(id string, callID string, usage providers.Usage) *providers.Response {
	return &providers.Response{
		ID:    id,
		Model: "test-model",
		Usage: usage,
		Choices: []providers.Choice{{
			Message: providers.Message{
				Role: "assistant",
				ToolCalls: []providers.ToolCall{{
					ID:       callID,
					Type:     "function",
					Function: providers.FunctionCall{Name: "get_answer", Arguments: `{"q":"?"}`},
				}},
			},
			FinishReason: "tool_calls",
		}},
	}
}

// A multi-turn MCP request must be accounted for as the SUM of its turns.
//
// runMCPLoop overwrites resp on every turn, so before this only the FINAL
// turn's usage ever left the loop — while the accumulated provider DURATION did,
// having been fixed for exactly this reason. Everything downstream reads
// resp.Usage: calculateCost prices it, recordSuccess adds it to the Prometheus
// token counters, and the after_request stage hands it to the request logger and
// to the budget guardrail's cost recording. A three-turn agentic request was
// therefore priced, metered and billed as though it had made one call — and
// under loadbalance or least-latency the dropped turns can be on a different
// provider entirely, so the spend was not merely undercounted but unattributed.
//
// The assertion is on the after_request view as well as the returned response,
// because that view is what budget and the request log actually read.
func TestRoute_MCPLoopAccountsForEveryTurnsUsage(t *testing.T) {
	mcpSrv := newMCPTestServer(t)
	defer mcpSrv.Close()

	final := &providers.Response{
		ID:    "resp-final",
		Model: "test-model",
		Usage: mcpUsageTurns[2],
		Choices: []providers.Choice{{
			Message:      providers.Message{Role: "assistant", Content: "The answer is 42."},
			FinishReason: "stop",
		}},
	}
	mp := &multiCallProvider{
		name:   "mcp-usage-provider",
		models: []string{"test-model"},
		responses: []*providers.Response{
			mcpToolCallTurn("resp-1", "tc-1", mcpUsageTurns[0]),
			mcpToolCallTurn("resp-2", "tc-2", mcpUsageTurns[1]),
			final,
		},
	}

	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "mcp-usage-provider"}},
		MCPServers: []mcp.ServerConfig{{
			Name:           "test-mcp",
			URL:            mcpSrv.URL + "/mcp",
			TimeoutSeconds: 5,
		}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(mp)

	// Price the target, so the cost assertion below is a claim about a figure
	// that can be non-zero rather than one that was always going to read zero.
	gw.catalog = models.Catalog{
		"mcp-usage-provider/test-model": {
			Provider: "mcp-usage-provider",
			ModelID:  "test-model",
			Mode:     models.ModeChat,
			Pricing: models.Pricing{
				InputPerMTokens:  ptrFloat64(3),
				OutputPerMTokens: ptrFloat64(15),
			},
		},
	}

	// Capture exactly what the after_request stage is handed — the view budget
	// prices from and the request logger persists.
	var (
		stageUsage providers.Usage
		stageCost  float64
		stageSeen  bool
	)
	if err := gw.RegisterPlugin(plugin.StageAfterRequest, &testPlugin{
		name: "usage-probe",
		typ:  plugin.TypeLogging,
		execFn: func(_ context.Context, pctx *plugin.Context) error {
			if pctx.Response != nil {
				stageUsage = pctx.Response.Usage
				stageCost = pctx.Measurements.CostUSD
				stageSeen = true
			}
			return nil
		},
	}); err != nil {
		t.Fatalf("RegisterPlugin: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	select {
	case <-gw.MCPInitDone():
	case <-ctx.Done():
		t.Fatal("timed out waiting for MCP initialization")
	}

	resp, err := gw.Route(ctx, providers.Request{
		Model:    "test-model",
		Messages: []providers.Message{{Role: "user", Content: "What is the answer?"}},
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}

	// Guard the probe: without three provider calls the sums below could match
	// the final turn by accident and the test would pass vacuously.
	if got := mp.callCount(); got != len(mcpUsageTurns) {
		t.Fatalf("provider called %d times, want %d — the loop did not run the turns this test measures", got, len(mcpUsageTurns))
	}

	want := mcpUsageTotal()
	if resp.Usage != want {
		t.Errorf("response usage = %+v, want the sum of every turn %+v (final turn alone is %+v)",
			resp.Usage, want, mcpUsageTurns[2])
	}
	if !stageSeen {
		t.Fatal("the after_request stage never saw a response; the cost and usage assertions below prove nothing")
	}
	if stageUsage != want {
		t.Errorf("after_request usage = %+v, want %+v — this is the view the budget guardrail bills and the request logger persists",
			stageUsage, want)
	}

	// 3 USD per M input + 15 USD per M output, over the summed usage.
	wantCost := 3*float64(want.PromptTokens)/1e6 + 15*float64(want.CompletionTokens)/1e6
	if math.Abs(stageCost-wantCost) > 1e-12 {
		t.Errorf("after_request cost = %v, want %v — the request was priced for one turn of a %d-turn request",
			stageCost, wantCost, len(mcpUsageTurns))
	}
}

// Configuring an MCP server must not switch the response cache off.
//
// The cache keys on tools, correctly, and computes that key twice: once to look
// up at before_request and once to store at after_request. The gateway injected
// its MCP tool definitions into the request BETWEEN those two points, and
// plugin.Context holds a pointer to that request — so the store key described a
// request the caller never sent and could never match the next lookup.
//
// The effect was total and silent: any mcp_servers entry disabled response
// caching for the whole deployment, with no tool call involved and the plugin
// still reporting itself configured.
func TestRoute_MCPInjectionDoesNotDisableTheResponseCache(t *testing.T) {
	mcpSrv := newMCPTestServer(t)
	defer mcpSrv.Close()

	var calls atomic.Int64
	gw, err := newTestGateway(t, config.Config{
		Strategy:   config.StrategyConfig{Mode: config.ModeSingle},
		Targets:    []config.Target{{VirtualKey: "mock-cache-mcp"}},
		MCPServers: []mcp.ServerConfig{{Name: "s1", URL: mcpSrv.URL, TimeoutSeconds: 5}},
		Plugins:    cacheEntries(),
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   "mock-cache-mcp",
		models: []string{"gpt-4o"},
		completeFn: func(context.Context, providers.Request) (*providers.Response, error) {
			calls.Add(1)
			return &providers.Response{
				ID:      "resp",
				Model:   "gpt-4o",
				Choices: []providers.Choice{{Message: providers.Message{Role: "assistant", Content: "hi"}}},
			}, nil
		},
	})

	if err := gw.LoadPlugins(); err != nil {
		t.Fatalf("load plugins: %v", err)
	}

	select {
	case <-gw.MCPInitDone():
	case <-time.After(10 * time.Second):
		t.Fatal("MCP init timeout")
	}

	// The registry must really be up, or MCP is inactive and this proves nothing.
	if gw.mcpRegistry == nil || len(gw.mcpRegistry.AllTools()) == 0 {
		t.Fatal("no MCP tools registered; the test would pass without exercising the defect")
	}

	ctx := authctx.WithKeyID(context.Background(), "key-a")
	for range 2 {
		if _, err := gw.Route(ctx, chainRequest()); err != nil {
			t.Fatalf("Route: %v", err)
		}
	}

	if got := calls.Load(); got != 1 {
		t.Errorf("provider calls = %d, want 1 — the second identical request must be served from cache", got)
	}
}

// B5: the gateway's environment is deliberately not inherited by MCP
// subprocesses, so ServerConfig.Env is the only channel by which a credential
// can reach one. Before this fix ${VAR} was resolved for Headers only, so a
// subprocess received the literal reference and there was no working way to
// pass a secret at all — which is what config.example.yaml's BRAVE_API_KEY
// entry depended on.
func TestResolveMCPServerRefs_ResolvesEnvAndHeaders(t *testing.T) {
	t.Setenv("FERRO_TEST_MCP_SECRET", "s3cret-value")
	t.Setenv("FERRO_TEST_MCP_TOKEN", "tok-123")

	in := mcp.ServerConfig{
		Name:    "srv",
		Command: "true",
		Env:     map[string]string{"API_KEY": "${FERRO_TEST_MCP_SECRET}"},
		Headers: map[string]string{"Authorization": "Bearer ${FERRO_TEST_MCP_TOKEN}"},
	}

	got, err := resolveMCPServerRefs(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Env["API_KEY"] != "s3cret-value" {
		t.Errorf("Env[API_KEY] = %q, want %q", got.Env["API_KEY"], "s3cret-value")
	}
	if got.Headers["Authorization"] != "Bearer tok-123" {
		t.Errorf("Headers[Authorization] = %q, want %q", got.Headers["Authorization"], "Bearer tok-123")
	}
}

// The caller's Config must keep the reference, never the resolved secret —
// otherwise the secret reaches the config-history store and GET /admin/config.
func TestResolveMCPServerRefs_DoesNotMutateInput(t *testing.T) {
	t.Setenv("FERRO_TEST_MCP_SECRET", "s3cret-value")

	env := map[string]string{"API_KEY": "${FERRO_TEST_MCP_SECRET}"}
	in := mcp.ServerConfig{Name: "srv", Command: "true", Env: env}

	if _, err := resolveMCPServerRefs(in); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if env["API_KEY"] != "${FERRO_TEST_MCP_SECRET}" {
		t.Errorf("input Env was mutated to %q; the resolved secret must not leak back into Config", env["API_KEY"])
	}
}

// An unresolvable reference in Env must fail the server the same way an
// unresolvable header does, rather than silently starting a subprocess with a
// broken credential.
func TestResolveMCPServerRefs_UndefinedEnvVarIsAnError(t *testing.T) {
	requireUnsetEnv(t, mcpUndefinedVar)

	in := mcp.ServerConfig{
		Name:    "srv",
		Command: "true",
		Env:     map[string]string{"API_KEY": "${" + mcpUndefinedVar + "}"},
	}

	if _, err := resolveMCPServerRefs(in); err == nil {
		t.Fatal("expected an error for an undefined ${VAR} in Env, got nil")
	}
}

// chunkStreamProvider emits several chunks so a test can tell real streaming
// from the MCP redirect, which collapses the whole response into one chunk.
type chunkStreamProvider struct {
	mockProvider
	chunks int
}

func (c *chunkStreamProvider) CompleteStream(ctx context.Context, _ providers.Request) (<-chan providers.StreamChunk, error) {
	ch := make(chan providers.StreamChunk, c.chunks)
	go func() {
		defer close(ch)
		for i := 0; i < c.chunks; i++ {
			select {
			case ch <- providers.StreamChunk{
				ID:    "chunk",
				Model: "gpt-4o",
				Choices: []providers.StreamChoice{{
					Delta: providers.MessageDelta{Content: "tok"},
				}},
			}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

// A configured-but-unreachable MCP server must not disable streaming.
//
// Regression guard for B1: RouteStream gated on Registry.HasServers(), which is
// true from the moment a server is *registered* — before, and regardless of,
// the handshake. One typo'd command or a dead URL therefore turned every
// stream:true request on the gateway into a fake single-chunk stream, forever,
// for every caller — including callers who never used MCP.
func TestGateway_RouteStream_BrokenMCPServerDoesNotDisableStreaming(t *testing.T) {
	const wantChunks = 3

	sp := &chunkStreamProvider{
		mockProvider: mockProvider{name: "mock-stream", models: []string{"gpt-4o"}},
		chunks:       wantChunks,
	}

	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "mock-stream"}},
		MCPServers: []mcp.ServerConfig{{
			// Reserved discard port: registration succeeds, the handshake never does.
			Name:           "dead-mcp",
			URL:            "http://127.0.0.1:1/mcp",
			TimeoutSeconds: 1,
		}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(sp)

	// Let initialization run and fail before routing.
	select {
	case <-gw.MCPInitDone():
	case <-time.After(10 * time.Second):
		t.Fatal("MCP init timeout")
	}

	ch, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Stream:   true,
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("RouteStream error: %v", err)
	}

	var got int
	for chunk := range ch {
		if chunk.Error != nil {
			t.Fatalf("stream chunk error: %v", chunk.Error)
		}
		got++
	}

	if got != wantChunks {
		t.Fatalf("got %d chunks, want %d — a dead MCP server collapsed the stream "+
			"(got 1 chunk means RouteStream took the MCP redirect)", got, wantChunks)
	}
}
