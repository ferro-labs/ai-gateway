package cohere

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// TestComplete_OmitsContentOnAssistantToolCall verifies #139 follow-up: an
// assistant turn that carries tool_calls and no text must omit "content"
// entirely. Content is `any` with omitempty, which does NOT drop an empty
// string, so without the guard Cohere v2 would receive content:"" and reject it.
func TestComplete_OmitsContentOnAssistantToolCall(t *testing.T) {
	var rawBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]},"finish_reason":"COMPLETE","usage":{"tokens":{"input_tokens":1,"output_tokens":1}}}`)
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.Complete(context.Background(), core.Request{
		Model: "command-r-plus",
		Messages: []core.Message{
			{Role: core.RoleUser, Content: "weather?"},
			{Role: core.RoleAssistant, ToolCalls: []core.ToolCall{{
				ID: "call_1", Type: "function",
				Function: core.FunctionCall{Name: "lookup", Arguments: `{"city":"SF"}`},
			}}},
			{Role: core.RoleTool, ToolCallID: "call_1", Content: `{"temp":"72F"}`},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	var body struct {
		Messages []map[string]json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(rawBody, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Messages) != 3 {
		t.Fatalf("messages len = %d, want 3", len(body.Messages))
	}
	// Index 1 is the assistant tool-call turn: it must have tool_calls and no content key.
	assistant := body.Messages[1]
	if _, ok := assistant["tool_calls"]; !ok {
		t.Fatalf("assistant message missing tool_calls: %v", assistant)
	}
	if c, ok := assistant["content"]; ok {
		t.Errorf("assistant tool-call message must omit content, got content=%s", c)
	}
}
