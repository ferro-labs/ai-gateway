package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// captureBody spins up a stub Anthropic server, runs one Complete, and returns
// the decoded request body the provider sent.
func captureBody(t *testing.T, req core.Request) map[string]json.RawMessage {
	t.Helper()
	var captured map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"claude","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer srv.Close()

	p, err := New("test-key", srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := p.Complete(context.Background(), req); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	return captured
}

// decodeMessages pulls the "messages" array out of a captured request body.
func decodeMessages(t *testing.T, body map[string]json.RawMessage) []map[string]json.RawMessage {
	t.Helper()
	var msgs []map[string]json.RawMessage
	if err := json.Unmarshal(body["messages"], &msgs); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	return msgs
}

// TestBuildMessages_VisionForwardsImageBase64 verifies #143: a multimodal user
// turn with a data-URI image is mapped to Anthropic content blocks (text +
// base64 image) instead of being collapsed to text-only.
func TestBuildMessages_VisionForwardsImageBase64(t *testing.T) {
	body := captureBody(t, core.Request{
		Model: "claude-3-5-sonnet",
		Messages: []core.Message{{
			Role: core.RoleUser,
			ContentParts: []core.ContentPart{
				{Type: "text", Text: "what is this"},
				{Type: "image_url", ImageURL: &core.ImageURLPart{URL: "data:image/png;base64,QUJD"}},
			},
		}},
	})

	msgs := decodeMessages(t, body)
	if len(msgs) != 1 {
		t.Fatalf("messages len = %d, want 1", len(msgs))
	}

	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(msgs[0]["content"], &blocks); err != nil {
		t.Fatalf("content is not a block array: %v (raw: %s)", err, msgs[0]["content"])
	}
	if len(blocks) != 2 {
		t.Fatalf("content blocks = %d, want 2", len(blocks))
	}
	if str(blocks[0]["type"]) != "text" || str(blocks[1]["type"]) != "image" {
		t.Fatalf("block types = [%s,%s], want [text,image]", blocks[0]["type"], blocks[1]["type"])
	}

	var source struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	}
	if err := json.Unmarshal(blocks[1]["source"], &source); err != nil {
		t.Fatalf("decode source: %v", err)
	}
	if source.Type != "base64" || source.MediaType != "image/png" || source.Data != "QUJD" {
		t.Errorf("image source = %+v, want base64/image/png/QUJD", source)
	}
}

// TestBuildMessages_VisionForwardsImageURL verifies an http(s) image URL maps to
// a url image source.
func TestBuildMessages_VisionForwardsImageURL(t *testing.T) {
	body := captureBody(t, core.Request{
		Model: "claude-3-5-sonnet",
		Messages: []core.Message{{
			Role:         core.RoleUser,
			ContentParts: []core.ContentPart{{Type: "image_url", ImageURL: &core.ImageURLPart{URL: "https://example.com/cat.png"}}},
		}},
	})

	msgs := decodeMessages(t, body)
	var blocks []map[string]json.RawMessage
	_ = json.Unmarshal(msgs[0]["content"], &blocks)
	var source struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	}
	if err := json.Unmarshal(blocks[0]["source"], &source); err != nil {
		t.Fatalf("decode source: %v", err)
	}
	if source.Type != "url" || source.URL != "https://example.com/cat.png" {
		t.Errorf("image source = %+v, want url/https://example.com/cat.png", source)
	}
}

// TestBuildMessages_ToolRoundTrip verifies #143: an assistant tool call and the
// following tool-role result are emitted as Anthropic tool_use / tool_result
// blocks (no bare {"role":"tool"} message that would 400).
func TestBuildMessages_ToolRoundTrip(t *testing.T) {
	body := captureBody(t, core.Request{
		Model: "claude-3-5-sonnet",
		Messages: []core.Message{
			{Role: core.RoleUser, Content: "weather in SF?"},
			{Role: core.RoleAssistant, ToolCalls: []core.ToolCall{{
				ID:       "toolu_1",
				Type:     "function",
				Function: core.FunctionCall{Name: "get_weather", Arguments: `{"city":"SF"}`},
			}}},
			{Role: core.RoleTool, ToolCallID: "toolu_1", Content: "72F"},
		},
	})

	msgs := decodeMessages(t, body)
	if len(msgs) != 3 {
		t.Fatalf("messages len = %d, want 3 (user, assistant, user/tool_result)", len(msgs))
	}

	// No bare tool-role message survives.
	for i, m := range msgs {
		if str(m["role"]) == "tool" {
			t.Fatalf("message %d still has role=tool", i)
		}
	}

	// Assistant turn carries a tool_use block.
	var aBlocks []map[string]json.RawMessage
	if err := json.Unmarshal(msgs[1]["content"], &aBlocks); err != nil {
		t.Fatalf("assistant content not blocks: %v", err)
	}
	if str(aBlocks[0]["type"]) != "tool_use" {
		t.Fatalf("assistant block type = %s, want tool_use", aBlocks[0]["type"])
	}
	if str(aBlocks[0]["id"]) != "toolu_1" || str(aBlocks[0]["name"]) != "get_weather" {
		t.Errorf("tool_use id/name = %s/%s", aBlocks[0]["id"], aBlocks[0]["name"])
	}
	var input map[string]string
	if err := json.Unmarshal(aBlocks[0]["input"], &input); err != nil {
		t.Fatalf("tool_use input not an object: %v (raw %s)", err, aBlocks[0]["input"])
	}
	if input["city"] != "SF" {
		t.Errorf("tool_use input city = %q, want SF", input["city"])
	}

	// Result becomes a user turn with a tool_result block.
	if str(msgs[2]["role"]) != "user" {
		t.Errorf("tool result role = %s, want user", msgs[2]["role"])
	}
	var rBlocks []map[string]json.RawMessage
	if err := json.Unmarshal(msgs[2]["content"], &rBlocks); err != nil {
		t.Fatalf("result content not blocks: %v", err)
	}
	if str(rBlocks[0]["type"]) != "tool_result" || str(rBlocks[0]["tool_use_id"]) != "toolu_1" {
		t.Errorf("tool_result block = %v", rBlocks[0])
	}
	if str(rBlocks[0]["content"]) != "72F" {
		t.Errorf("tool_result content = %s, want 72F", rBlocks[0]["content"])
	}
}

func TestComplete_ForwardsToolsAndDecodesToolUse(t *testing.T) {
	var captured map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"msg_1",
			"type":"message",
			"role":"assistant",
			"content":[{"type":"tool_use","id":"toolu_1","name":"lookup","input":{"city":"SF"}}],
			"model":"claude",
			"stop_reason":"tool_use",
			"usage":{"input_tokens":1,"output_tokens":1}
		}`)
	}))
	defer srv.Close()

	p, err := New("test-key", srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "claude-3-5-sonnet",
		Messages: []core.Message{{Role: core.RoleUser, Content: "weather?"}},
		Tools: []core.Tool{{
			Type: "function",
			Function: core.Function{
				Name:        "lookup",
				Description: "Lookup weather",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
			},
		}},
		ToolChoice: "required",
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	var tools []map[string]json.RawMessage
	if err := json.Unmarshal(captured["tools"], &tools); err != nil {
		t.Fatalf("decode tools: %v", err)
	}
	if len(tools) != 1 || str(tools[0]["name"]) != "lookup" {
		t.Fatalf("tools = %#v, want lookup", tools)
	}
	var choice map[string]string
	if err := json.Unmarshal(captured["tool_choice"], &choice); err != nil {
		t.Fatalf("decode tool_choice: %v", err)
	}
	if choice["type"] != "any" {
		t.Fatalf("tool_choice = %#v, want any", choice)
	}
	if len(resp.Choices) != 1 || len(resp.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v, want one", resp.Choices)
	}
	tc := resp.Choices[0].Message.ToolCalls[0]
	if tc.ID != "toolu_1" || tc.Function.Name != "lookup" || tc.Function.Arguments != `{"city":"SF"}` {
		t.Fatalf("tool call = %#v, want lookup", tc)
	}
}

func TestComplete_ForwardsToolResultAndDecodesFinalAnswer(t *testing.T) {
	var captured map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"msg_2",
			"type":"message",
			"role":"assistant",
			"content":[{"type":"text","text":"It is 72F in SF."}],
			"model":"claude",
			"stop_reason":"end_turn",
			"usage":{"input_tokens":2,"output_tokens":3}
		}`)
	}))
	defer srv.Close()

	p, err := New("test-key", srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := p.Complete(context.Background(), core.Request{
		Model: "claude-3-5-sonnet",
		Messages: []core.Message{
			{Role: core.RoleUser, Content: "weather?"},
			{Role: core.RoleAssistant, ToolCalls: []core.ToolCall{{
				ID:       "toolu_1",
				Type:     "function",
				Function: core.FunctionCall{Name: "lookup", Arguments: `{"city":"SF"}`},
			}}},
			{Role: core.RoleTool, ToolCallID: "toolu_1", Content: `{"temp":"72F"}`},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	msgs := decodeMessages(t, captured)
	if len(msgs) != 3 {
		t.Fatalf("messages len = %d, want 3", len(msgs))
	}
	var resultBlocks []map[string]json.RawMessage
	if err := json.Unmarshal(msgs[2]["content"], &resultBlocks); err != nil {
		t.Fatalf("decode tool result blocks: %v", err)
	}
	if str(msgs[2]["role"]) != core.RoleUser || str(resultBlocks[0]["type"]) != "tool_result" || str(resultBlocks[0]["tool_use_id"]) != "toolu_1" {
		t.Fatalf("tool result message = %#v blocks=%#v", msgs[2], resultBlocks)
	}
	if got := resp.Choices[0].Message.Content; got != "It is 72F in SF." {
		t.Fatalf("final answer = %q, want weather answer", got)
	}
	if len(resp.Choices[0].Message.ToolCalls) != 0 {
		t.Fatalf("final answer tool calls = %#v, want none", resp.Choices[0].Message.ToolCalls)
	}
}

// TestBuildMessages_PlainTextStaysString verifies the common path is unchanged:
// a plain-text turn still serializes content as a JSON string.
func TestBuildMessages_PlainTextStaysString(t *testing.T) {
	body := captureBody(t, core.Request{
		Model:    "claude-3-5-sonnet",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hello"}},
	})
	msgs := decodeMessages(t, body)
	var s string
	if err := json.Unmarshal(msgs[0]["content"], &s); err != nil {
		t.Fatalf("plain content should be a string, got %s", msgs[0]["content"])
	}
	if s != "hello" {
		t.Errorf("content = %q, want hello", s)
	}
}

// str unwraps a JSON string token to its Go value for terse assertions.
func str(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}
