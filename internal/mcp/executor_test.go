package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// buildReadyRegistry creates a Registry backed by a single mock MCP server
// that exposes the given tool names. InitializeAll is called before returning.
func buildReadyRegistry(t *testing.T, toolNames []string) *Registry {
	t.Helper()
	tools := make([]Tool, len(toolNames))
	for i, n := range toolNames {
		tools[i] = Tool{Name: n}
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case mcpMethodInitialize:
			w.Header().Set("Mcp-Session-Id", "sid-exec-test")
			_ = json.NewEncoder(w).Encode(JSONRPCResponse{
				JSONRPC: "2.0", ID: req.ID,
				Result: mustMarshal(ServerInfo{Name: "mock-exec", Version: "1"}),
			})
		case mcpMethodToolsList:
			_ = json.NewEncoder(w).Encode(JSONRPCResponse{
				JSONRPC: "2.0", ID: req.ID,
				Result: mustMarshal(map[string]any{"tools": tools}),
			})
		case mcpMethodToolsCall:
			_ = json.NewEncoder(w).Encode(JSONRPCResponse{
				JSONRPC: "2.0", ID: req.ID,
				Result: mustMarshal(ToolCallResult{
					Content: []ContentBlock{{Type: "text", Text: "ok-result"}},
				}),
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	reg := NewRegistry()
	reg.RegisterConfig(ServerConfig{Name: "exec-srv", URL: srv.URL, TimeoutSeconds: 5})
	reg.InitializeAll(context.Background(), func(name string, err error) {
		t.Errorf("init error for %s: %v", name, err)
	})
	return reg
}

// ---------------------------------------------------------------------------
// ShouldContinueLoop

func TestShouldContinueLoopNilResponse(t *testing.T) {
	exec := NewExecutor(NewRegistry(), 5)
	if exec.ShouldContinueLoop(nil, 0) {
		t.Error("expected false for nil response")
	}
}

func TestShouldContinueLoopDepthExceeded(t *testing.T) {
	exec := NewExecutor(NewRegistry(), 3)
	resp := &core.Response{
		Choices: []core.Choice{{
			Message: core.Message{ToolCalls: []core.ToolCall{{ID: "1"}}},
		}},
	}
	if exec.ShouldContinueLoop(resp, 3) {
		t.Error("expected false when depth == maxCallDepth")
	}
}

func TestShouldContinueLoopNoToolCalls(t *testing.T) {
	exec := NewExecutor(NewRegistry(), 5)
	resp := &core.Response{
		Choices: []core.Choice{{
			Message: core.Message{Content: "just text"},
		}},
	}
	if exec.ShouldContinueLoop(resp, 0) {
		t.Error("expected false when no tool calls")
	}
}

func TestShouldContinueLoopWithToolCalls(t *testing.T) {
	exec := NewExecutor(NewRegistry(), 5)
	resp := &core.Response{
		Choices: []core.Choice{{
			Message: core.Message{
				ToolCalls: []core.ToolCall{{ID: "tc1", Type: "function",
					Function: core.FunctionCall{Name: "do_thing", Arguments: "{}"}}},
			},
		}},
	}
	if !exec.ShouldContinueLoop(resp, 0) {
		t.Error("expected true when tool calls present and depth < max")
	}
}

// ---------------------------------------------------------------------------
// ResolvePendingToolCalls

func TestResolvePendingToolCallsNilResponse(t *testing.T) {
	exec := NewExecutor(NewRegistry(), 5)
	msgs, err := exec.ResolvePendingToolCalls(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgs != nil {
		t.Errorf("expected nil messages for nil response, got %v", msgs)
	}
}

func TestResolvePendingToolCallsFoundTool(t *testing.T) {
	reg := buildReadyRegistry(t, []string{"do_thing"})
	exec := NewExecutor(reg, 5)

	resp := &core.Response{
		Choices: []core.Choice{{
			Message: core.Message{
				ToolCalls: []core.ToolCall{{
					ID:       "call-1",
					Type:     "function",
					Function: core.FunctionCall{Name: "do_thing", Arguments: `{"x":1}`},
				}},
			},
		}},
	}

	msgs, err := exec.ResolvePendingToolCalls(context.Background(), resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Expect: 1 assistant message + 1 tool result message
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d: %v", len(msgs), msgs)
	}
	if msgs[0].Role != "assistant" {
		t.Errorf("first message should be assistant, got %q", msgs[0].Role)
	}
	if msgs[1].Role != core.RoleTool {
		t.Errorf("second message should be tool, got %q", msgs[1].Role)
	}
	if msgs[1].ToolCallID != "call-1" {
		t.Errorf("wrong ToolCallID: %q", msgs[1].ToolCallID)
	}
	if msgs[1].Content != "ok-result" {
		t.Errorf("unexpected content: %q", msgs[1].Content)
	}
}

func TestResolvePendingToolCallsUnknownTool(t *testing.T) {
	exec := NewExecutor(NewRegistry(), 5) // empty registry

	resp := &core.Response{
		Choices: []core.Choice{{
			Message: core.Message{
				ToolCalls: []core.ToolCall{{
					ID:       "ghost",
					Type:     "function",
					Function: core.FunctionCall{Name: "no_such_tool", Arguments: "{}"},
				}},
			},
		}},
	}

	msgs, err := exec.ResolvePendingToolCalls(context.Background(), resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (assistant + error tool result), got %d", len(msgs))
	}
	// Tool result should embed an error JSON
	if msgs[1].Role != core.RoleTool {
		t.Errorf("expected role tool, got %q", msgs[1].Role)
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(msgs[1].Content), &payload); err != nil {
		t.Fatalf("tool result content is not valid JSON: %v — content: %q", err, msgs[1].Content)
	}
	if payload["error"] == "" {
		t.Error("expected error field in tool result content")
	}
}

func TestResolvePendingToolCallsServerError(t *testing.T) {
	// Server that succeeds initialize/list but fails tool calls
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req JSONRPCRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case mcpMethodInitialize:
			w.Header().Set("Mcp-Session-Id", "sid-err")
			_ = json.NewEncoder(w).Encode(JSONRPCResponse{
				JSONRPC: "2.0", ID: req.ID,
				Result: mustMarshal(ServerInfo{Name: "err-srv", Version: "1"}),
			})
		case mcpMethodToolsList:
			_ = json.NewEncoder(w).Encode(JSONRPCResponse{
				JSONRPC: "2.0", ID: req.ID,
				Result: mustMarshal(map[string]any{"tools": []Tool{{Name: "boom"}}}),
			})
		default:
			http.Error(w, "fail", http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	reg := NewRegistry()
	reg.RegisterConfig(ServerConfig{Name: "err-srv", URL: srv.URL, TimeoutSeconds: 5})
	reg.InitializeAll(context.Background(), nil)

	exec := NewExecutor(reg, 5)
	resp := &core.Response{
		Choices: []core.Choice{{
			Message: core.Message{
				ToolCalls: []core.ToolCall{{
					ID:       "err-call",
					Type:     "function",
					Function: core.FunctionCall{Name: "boom", Arguments: "{}"},
				}},
			},
		}},
	}

	msgs, err := exec.ResolvePendingToolCalls(context.Background(), resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	// Tool result must contain an error
	if msgs[1].Content == "" {
		t.Error("expected non-empty error content")
	}
}

func init() { _ = time.Second } // keep time import used
