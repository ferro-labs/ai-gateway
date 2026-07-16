package aigateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/mcp"
	"github.com/ferro-labs/ai-gateway/providers"
)

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

	gw, err := newTestGateway(t, Config{
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

	gw, err := newTestGateway(t, Config{
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
