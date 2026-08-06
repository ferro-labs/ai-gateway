package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
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
				Result: initializeResult("mock-exec", "1"),
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

	reg := NewRegistry(nil)
	reg.RegisterConfig(ServerConfig{Name: "exec-srv", URL: srv.URL, TimeoutSeconds: 5})
	reg.InitializeAll(context.Background(), func(name string, err error) {
		t.Errorf("init error for %s: %v", name, err)
	})
	return reg
}

// ---------------------------------------------------------------------------
// ShouldContinueLoop

func TestShouldContinueLoopNilResponse(t *testing.T) {
	exec := NewExecutor(NewRegistry(nil), 5, nil)
	if exec.ShouldContinueLoop(nil, 0) {
		t.Error("expected false for nil response")
	}
}

func TestShouldContinueLoopDepthExceeded(t *testing.T) {
	exec := NewExecutor(NewRegistry(nil), 3, nil)
	resp := &core.Response{
		Choices: []core.Choice{{
			Message: core.Message{ToolCalls: []core.ToolCall{{ID: "1"}}},
		}},
	}
	if exec.ShouldContinueLoop(resp, 3) {
		t.Error("expected false when depth == maxCallDepth")
	}
}

// Reaching the depth limit is an outcome, not a silent stop: the response still
// names tools the gateway injected and the client never declared, so handing it
// back with finish_reason tool_calls asks the client to run something it has no
// way to run.
func TestShouldContinueLoopAtDepthLimitDropsUnrunnableToolCalls(t *testing.T) {
	exec := NewExecutor(readyRegistry(t, []Tool{{Name: "mcp_tool"}}), 3, nil)
	resp := toolCallResponse("mcp_tool")
	resp.Choices[0].FinishReason = core.FinishReasonToolCalls

	if exec.ShouldContinueLoop(resp, 3) {
		t.Fatal("loop continued at the depth limit")
	}
	if calls := resp.Choices[0].Message.ToolCalls; len(calls) != 0 {
		t.Errorf("client was handed %d unexecuted MCP tool call(s) it cannot satisfy: %+v", len(calls), calls)
	}
	if got := resp.Choices[0].FinishReason; got != core.FinishReasonLength {
		t.Errorf("finish_reason = %q, want %q so the client sees a truncated answer", got, core.FinishReasonLength)
	}
}

// The client's own tool calls survive the limit untouched — those are the ones
// it declared and is waiting to answer.
func TestShouldContinueLoopAtDepthLimitKeepsCallerToolCalls(t *testing.T) {
	exec := NewExecutor(readyRegistry(t, []Tool{{Name: "mcp_tool"}}), 3, nil)
	resp := toolCallResponse("caller_tool")
	resp.Choices[0].FinishReason = core.FinishReasonToolCalls

	if exec.ShouldContinueLoop(resp, 3) {
		t.Fatal("loop continued at the depth limit")
	}
	if len(resp.Choices[0].Message.ToolCalls) != 1 {
		t.Error("a caller-supplied tool call was dropped; the client can only answer what it receives")
	}
	if got := resp.Choices[0].FinishReason; got != core.FinishReasonToolCalls {
		t.Errorf("finish_reason = %q, want it left at %q", got, core.FinishReasonToolCalls)
	}
}

func TestShouldContinueLoopNoToolCalls(t *testing.T) {
	exec := NewExecutor(NewRegistry(nil), 5, nil)
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
	// The loop arms only for tool calls MCP actually owns. Previously an empty
	// registry armed it for any tool name, which is what let the executor
	// hijack caller-supplied tool calls.
	exec := NewExecutor(readyRegistry(t, []Tool{{Name: "do_thing"}}), 5, nil)
	resp := &core.Response{
		Choices: []core.Choice{{
			Message: core.Message{
				ToolCalls: []core.ToolCall{{ID: "tc1", Type: "function",
					Function: core.FunctionCall{Name: "do_thing", Arguments: "{}"}}},
			},
		}},
	}
	if !exec.ShouldContinueLoop(resp, 0) {
		t.Error("expected true when an MCP-owned tool call is present and depth < max")
	}
}

// ---------------------------------------------------------------------------
// ResolvePendingToolCalls

func TestResolvePendingToolCallsNilResponse(t *testing.T) {
	exec := NewExecutor(NewRegistry(nil), 5, nil)
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
	exec := NewExecutor(reg, 5, nil)

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

// countingClient records the tool calls that actually reached a server.
type countingClient struct {
	mu    sync.Mutex
	calls []string
}

func (c *countingClient) Initialize(context.Context) (*ServerInfo, error) { return &ServerInfo{}, nil }
func (c *countingClient) ListTools(context.Context) ([]Tool, error)       { return nil, nil }
func (c *countingClient) CallTool(_ context.Context, name string, _ json.RawMessage) (*ToolCallResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, name)
	return &ToolCallResult{Content: []ContentBlock{{Type: "text", Text: "ok"}}}, nil
}
func (c *countingClient) Close() error { return nil }

// callCount reports how many tool calls the server received.
func (c *countingClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

// With n > 1 the provider returns alternative completions of one turn, each
// possibly asking for the same tool. Executing every choice sends the email once
// per alternative and then flattens the results of branches the conversation
// never takes into a single continuation.
func TestResolvePendingToolCallsExecutesOneChoiceOnly(t *testing.T) {
	client := &countingClient{}
	reg := registryWith(map[string]mcpClient{"s1": client})
	reg.mu.Lock()
	reg.servers["s1"].tools = []Tool{{Name: "send_email"}}
	reg.toolMap["send_email"] = "s1"
	reg.mu.Unlock()

	exec := NewExecutor(reg, 5, nil)
	alternative := func(index int, id string) core.Choice {
		return core.Choice{
			Index:        index,
			FinishReason: core.FinishReasonToolCalls,
			Message: core.Message{
				Role: core.RoleAssistant,
				ToolCalls: []core.ToolCall{{
					ID: id, Type: "function",
					Function: core.FunctionCall{Name: "send_email", Arguments: "{}"},
				}},
			},
		}
	}
	resp := &core.Response{Choices: []core.Choice{alternative(0, "call-a"), alternative(1, "call-b")}}

	extra, err := exec.ResolvePendingToolCalls(context.Background(), resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := client.callCount(); got != 1 {
		t.Errorf("send_email executed %d times for %d alternative completions, want 1", got, len(resp.Choices))
	}
	// One assistant message plus its one tool result: the continuation follows a
	// single completion, not a merge of several.
	if len(extra) != 2 {
		t.Fatalf("expected 2 messages for one continued completion, got %d: %+v", len(extra), extra)
	}
	assertProtocolComplete(t, extra)
}

func TestResolvePendingToolCallsUnownedToolPassesThrough(t *testing.T) {
	// A tool no MCP server owns belongs to the caller. The executor must not
	// answer it — it previously fabricated
	// {"error":"tool X not found in any registered MCP server"}, which swallowed
	// the client's own function calls and hid finish_reason: tool_calls.
	exec := NewExecutor(NewRegistry(nil), 5, nil) // empty registry

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
	if len(msgs) != 0 {
		t.Fatalf("expected 0 messages for an unowned tool call (caller executes it), got %d: %+v", len(msgs), msgs)
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
				Result: initializeResult("err-srv", "1"),
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

	reg := NewRegistry(nil)
	reg.RegisterConfig(ServerConfig{Name: "err-srv", URL: srv.URL, TimeoutSeconds: 5})
	reg.InitializeAll(context.Background(), nil)

	exec := NewExecutor(reg, 5, nil)
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

func TestResolvePendingToolCallsAuditFnCalledOnSuccess(t *testing.T) {
	type auditCall struct {
		serverName, toolName, status, errMsg string
		latencyMs                            int
	}
	called := make(chan auditCall, 1)
	auditFn := AuditFn(func(_ context.Context, sn, tn, st string, lms int, em string) {
		called <- auditCall{sn, tn, st, em, lms}
	})

	reg := buildReadyRegistry(t, []string{"do_thing"})
	exec := NewExecutor(reg, 5, auditFn)
	resp := &core.Response{
		Choices: []core.Choice{{
			Message: core.Message{
				ToolCalls: []core.ToolCall{{
					ID:       "c1",
					Type:     "function",
					Function: core.FunctionCall{Name: "do_thing", Arguments: "{}"},
				}},
			},
		}},
	}

	_, err := exec.ResolvePendingToolCalls(context.Background(), resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case got := <-called:
		if got.serverName != "exec-srv" {
			t.Errorf("serverName = %q, want %q", got.serverName, "exec-srv")
		}
		if got.toolName != "do_thing" {
			t.Errorf("toolName = %q, want %q", got.toolName, "do_thing")
		}
		if got.status != "ok" {
			t.Errorf("status = %q, want %q", got.status, "ok")
		}
		if got.errMsg != "" {
			t.Errorf("errMsg = %q, want empty", got.errMsg)
		}
		if got.latencyMs < 0 {
			t.Errorf("latencyMs = %d, want >= 0", got.latencyMs)
		}
	case <-time.After(time.Second):
		t.Fatal("auditFn was not called within 1s")
	}
}

func TestResolvePendingToolCallsAuditFnCalledOnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req JSONRPCRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case mcpMethodInitialize:
			w.Header().Set("Mcp-Session-Id", "sid-err2")
			_ = json.NewEncoder(w).Encode(JSONRPCResponse{
				JSONRPC: "2.0", ID: req.ID,
				Result: initializeResult("err2-srv", "1"),
			})
		case mcpMethodToolsList:
			_ = json.NewEncoder(w).Encode(JSONRPCResponse{
				JSONRPC: "2.0", ID: req.ID,
				Result: mustMarshal(map[string]any{"tools": []Tool{{Name: "fail_tool"}}}),
			})
		default:
			http.Error(w, "fail", http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	type auditCall struct {
		status, errMsg string
	}
	called := make(chan auditCall, 1)
	auditFn := AuditFn(func(_ context.Context, _, _, st string, _ int, em string) {
		called <- auditCall{st, em}
	})

	reg := NewRegistry(nil)
	reg.RegisterConfig(ServerConfig{Name: "err2-srv", URL: srv.URL, TimeoutSeconds: 5})
	reg.InitializeAll(context.Background(), nil)

	exec := NewExecutor(reg, 5, auditFn)
	resp := &core.Response{
		Choices: []core.Choice{{
			Message: core.Message{
				ToolCalls: []core.ToolCall{{
					ID:       "c2",
					Type:     "function",
					Function: core.FunctionCall{Name: "fail_tool", Arguments: "{}"},
				}},
			},
		}},
	}

	_, err := exec.ResolvePendingToolCalls(context.Background(), resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case got := <-called:
		if got.status != "error" {
			t.Errorf("status = %q, want %q", got.status, "error")
		}
		if got.errMsg == "" {
			t.Error("errMsg is empty, want non-empty error message")
		}
	case <-time.After(time.Second):
		t.Fatal("auditFn was not called within 1s")
	}
}

func TestResolvePendingToolCallsPanicInAuditFnDoesNotCrash(t *testing.T) {
	// panicked closes as the audit goroutine unwinds through auditFn's panic
	// (defer runs during unwinding, before the executor's recover), giving a
	// deterministic signal that the panic-guard path was exercised. A missing
	// recover would crash the test binary instead.
	panicked := make(chan struct{})
	auditFn := AuditFn(func(_ context.Context, _, _, _ string, _ int, _ string) {
		defer close(panicked)
		panic("boom — panic from user-supplied auditFn")
	})

	reg := buildReadyRegistry(t, []string{"do_thing"})
	exec := NewExecutor(reg, 5, auditFn)
	resp := &core.Response{
		Choices: []core.Choice{{
			Message: core.Message{
				ToolCalls: []core.ToolCall{{
					ID:       "c3",
					Type:     "function",
					Function: core.FunctionCall{Name: "do_thing", Arguments: "{}"},
				}},
			},
		}},
	}

	// The call must complete without panicking.
	_, err := exec.ResolvePendingToolCalls(context.Background(), resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Wait deterministically for the audit goroutine to enter (and panic through)
	// auditFn, instead of sleeping and hoping it scheduled in time.
	select {
	case <-panicked:
	case <-time.After(time.Second):
		t.Fatal("audit goroutine did not invoke auditFn")
	}
}
