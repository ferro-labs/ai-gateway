package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// A caller that supplies its own tools array (the ordinary OpenAI
// function-calling pattern) intends to execute those calls itself. The MCP
// executor must leave them alone: swallowing them and answering with a
// fabricated "not found" breaks every client-side integration the moment one
// MCP server is enabled.

func readyRegistry(t *testing.T, tools []Tool) *Registry {
	t.Helper()
	srv := newMockServer(t, tools)
	t.Cleanup(srv.Close)

	reg := NewRegistry()
	reg.RegisterConfig(ServerConfig{Name: "s1", URL: srv.URL, TimeoutSeconds: 5})
	reg.InitializeAll(context.Background(), func(name string, err error) {
		t.Fatalf("init %s: %v", name, err)
	})
	return reg
}

func toolCallResponse(names ...string) *core.Response {
	// IDs must be unique: the protocol-completeness check pairs each
	// tool_call_id with exactly one tool result.
	calls := make([]core.ToolCall, 0, len(names))
	for i, n := range names {
		calls = append(calls, core.ToolCall{
			ID:       fmt.Sprintf("call-%d-%s", i, n),
			Type:     "function",
			Function: core.FunctionCall{Name: n, Arguments: "{}"},
		})
	}
	return &core.Response{Choices: []core.Choice{{Message: core.Message{
		Role:      core.RoleAssistant,
		ToolCalls: calls,
	}}}}
}

func TestRegistryOwns(t *testing.T) {
	reg := readyRegistry(t, []Tool{{Name: "mcp_tool"}})

	if !reg.Owns("mcp_tool") {
		t.Error("Owns(mcp_tool) = false, want true for a ready indexed tool")
	}
	if reg.Owns("caller_tool") {
		t.Error("Owns(caller_tool) = true, want false for an unknown tool")
	}
}

func TestExecutorSkipsCallerOwnedToolCalls(t *testing.T) {
	reg := readyRegistry(t, []Tool{{Name: "mcp_tool"}})
	exec := NewExecutor(reg, 5, nil)

	resp := toolCallResponse("caller_tool")
	extra, err := exec.ResolvePendingToolCalls(context.Background(), resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(extra) != 0 {
		t.Fatalf("expected no messages for a caller-owned tool call, got %d: %+v", len(extra), extra)
	}
}

// assertProtocolComplete checks the invariant every OpenAI-compatible provider
// enforces: an assistant message carrying tool_calls must be followed by exactly
// one tool message per tool_call_id. A continuation that violates this is
// rejected outright, so a partially-answered turn is not a degraded result — it
// is a failed request.
func assertProtocolComplete(t *testing.T, msgs []core.Message) {
	t.Helper()
	answered := make(map[string]int)
	for _, m := range msgs {
		if m.Role == core.RoleTool {
			answered[m.ToolCallID]++
		}
	}
	for _, m := range msgs {
		if m.Role != core.RoleAssistant {
			continue
		}
		for _, tc := range m.ToolCalls {
			switch answered[tc.ID] {
			case 1:
				delete(answered, tc.ID)
			case 0:
				t.Errorf("tool_call %q has no tool result: the provider will reject this continuation", tc.ID)
			default:
				t.Errorf("tool_call %q answered %d times", tc.ID, answered[tc.ID])
			}
		}
	}
	for id := range answered {
		t.Errorf("tool result for %q has no matching tool_call in any assistant message", id)
	}
}

// A turn mixing MCP-owned and caller-owned calls cannot be satisfied by the
// gateway: it can answer only its own, and an assistant message with an
// unanswered tool_call_id is rejected by the provider. Executing the owned half
// and leaving the caller's unanswered turns a working request into a 400, so the
// whole turn is handed back to the client untouched.
func TestExecutorSkipsMixedOwnershipTurn(t *testing.T) {
	reg := readyRegistry(t, []Tool{{Name: "mcp_tool"}})
	exec := NewExecutor(reg, 5, nil)

	resp := toolCallResponse("mcp_tool", "caller_tool")
	extra, err := exec.ResolvePendingToolCalls(context.Background(), resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(extra) != 0 {
		t.Fatalf("expected a mixed-ownership turn to contribute no messages, got %d: %+v", len(extra), extra)
	}
	assertProtocolComplete(t, extra)
}

// The fully-owned case still runs, and must be protocol-complete.
func TestExecutorResolvesFullyOwnedTurn(t *testing.T) {
	reg := readyRegistry(t, []Tool{{Name: "mcp_tool"}, {Name: "mcp_other"}})
	exec := NewExecutor(reg, 5, nil)

	extra, err := exec.ResolvePendingToolCalls(context.Background(), toolCallResponse("mcp_tool", "mcp_other"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var toolMsgs int
	for _, m := range extra {
		if m.Role == core.RoleTool {
			toolMsgs++
		}
	}
	if toolMsgs != 2 {
		t.Fatalf("expected 2 tool results for 2 owned calls, got %d", toolMsgs)
	}
	assertProtocolComplete(t, extra)

	// No fabricated MCP errors on a healthy path.
	for _, m := range extra {
		if m.Role != core.RoleTool {
			continue
		}
		var payload map[string]string
		if json.Unmarshal([]byte(m.Content), &payload) == nil {
			if payload["error"] != "" {
				t.Errorf("unexpected error result for an owned call: %s", m.Content)
			}
		}
	}
}

func TestShouldContinueLoopIgnoresUnownedCalls(t *testing.T) {
	reg := readyRegistry(t, []Tool{{Name: "mcp_tool"}})
	exec := NewExecutor(reg, 5, nil)

	if exec.ShouldContinueLoop(toolCallResponse("caller_tool"), 0) {
		t.Error("loop armed for a caller-owned tool call; the client must receive finish_reason tool_calls")
	}
	if !exec.ShouldContinueLoop(toolCallResponse("mcp_tool"), 0) {
		t.Error("loop not armed for an MCP-owned tool call")
	}
	// Mixed ownership cannot be continued: the gateway can answer only its own
	// calls, and a partially-answered assistant turn is rejected by the provider.
	if exec.ShouldContinueLoop(toolCallResponse("caller_tool", "mcp_tool"), 0) {
		t.Error("loop armed for a mixed-ownership turn, which the gateway cannot satisfy")
	}
}

// R2: a response carrying an absurd number of tool calls must not translate
// into an unbounded number of executions and conversation messages.
func TestResolvePendingToolCallsIsBoundedPerTurn(t *testing.T) {
	reg := readyRegistry(t, []Tool{{Name: "mcp_tool"}})
	exec := NewExecutor(reg, 1, nil)

	names := make([]string, 0, maxToolCallsPerTurn+50)
	for i := 0; i < maxToolCallsPerTurn+50; i++ {
		names = append(names, "mcp_tool")
	}

	extra, err := exec.ResolvePendingToolCalls(context.Background(), toolCallResponse(names...))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var toolMsgs int
	for _, m := range extra {
		if m.Role == core.RoleTool {
			toolMsgs++
		}
	}
	if toolMsgs > maxToolCallsPerTurn {
		t.Errorf("executed %d tool calls in one turn, want at most %d", toolMsgs, maxToolCallsPerTurn)
	}

	// Truncating the executions is not enough: the assistant message must be
	// truncated with them, or the continuation carries tool_call_ids nothing
	// answers and the provider rejects the whole request.
	assertProtocolComplete(t, extra)
}
