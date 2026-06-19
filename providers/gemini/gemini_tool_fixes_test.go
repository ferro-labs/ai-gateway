package gemini

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// captureGeminiBody runs one Complete against a stub and returns the request body.
func captureGeminiBody(t *testing.T, req core.Request) map[string]json.RawMessage {
	t.Helper()
	var captured map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`))
	}))
	defer srv.Close()
	p, _ := New("test-key", srv.URL)
	if _, err := p.Complete(context.Background(), req); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	return captured
}

// TestComplete_SanitizesToolSchema verifies #139 follow-up: JSON-schema keywords
// Gemini's OpenAPI subset rejects (e.g. additionalProperties from OpenAI
// strict-mode tools, $schema) are stripped before forwarding.
func TestComplete_SanitizesToolSchema(t *testing.T) {
	captured := captureGeminiBody(t, core.Request{
		Model:    "gemini-1.5-pro",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
		Tools: []core.Tool{{
			Type: "function",
			Function: core.Function{
				Name:       "lookup",
				Parameters: json.RawMessage(`{"type":"object","$schema":"http://json-schema.org/draft-07/schema#","additionalProperties":false,"properties":{"city":{"type":"string","additionalProperties":false}}}`),
			},
		}},
	})

	var body struct {
		Tools []struct {
			FunctionDeclarations []struct {
				Parameters map[string]json.RawMessage `json:"parameters"`
			} `json:"functionDeclarations"`
		} `json:"tools"`
	}
	raw, _ := json.Marshal(captured)
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	params := body.Tools[0].FunctionDeclarations[0].Parameters
	if _, ok := params["additionalProperties"]; ok {
		t.Errorf("additionalProperties must be stripped, got %v", params)
	}
	if _, ok := params["$schema"]; ok {
		t.Errorf("$schema must be stripped, got %v", params)
	}
	// Nested additionalProperties must be stripped too.
	if props, ok := params["properties"]; ok {
		var nested map[string]map[string]json.RawMessage
		_ = json.Unmarshal(props, &nested)
		if _, ok := nested["city"]["additionalProperties"]; ok {
			t.Errorf("nested additionalProperties must be stripped, got %v", nested)
		}
	}
	// Legitimate keywords survive.
	if _, ok := params["type"]; !ok {
		t.Errorf("type must be preserved, got %v", params)
	}
}

// TestComplete_CoalescesParallelToolResults verifies #139 follow-up: parallel
// tool results (each its own role="tool" message) are merged into a single
// user content with multiple functionResponse parts, preserving Gemini's
// strict user/model alternation.
func TestComplete_CoalescesParallelToolResults(t *testing.T) {
	captured := captureGeminiBody(t, core.Request{
		Model: "gemini-2.0-flash",
		Messages: []core.Message{
			{Role: core.RoleUser, Content: "weather + time?"},
			{Role: core.RoleAssistant, ToolCalls: []core.ToolCall{
				{ID: "c1", Type: "function", Function: core.FunctionCall{Name: "weather", Arguments: `{"city":"SF"}`}},
				{ID: "c2", Type: "function", Function: core.FunctionCall{Name: "clock", Arguments: `{"city":"SF"}`}},
			}},
			{Role: core.RoleTool, ToolCallID: "c1", Content: `{"temp":72}`},
			{Role: core.RoleTool, ToolCallID: "c2", Content: `{"time":"3pm"}`},
		},
	})

	var body struct {
		Contents []struct {
			Role  string `json:"role"`
			Parts []struct {
				FunctionResponse *struct {
					Name string `json:"name"`
				} `json:"functionResponse"`
			} `json:"parts"`
		} `json:"contents"`
	}
	raw, _ := json.Marshal(captured)
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	// Expect: [user "weather+time?", model (2 functionCall), user (2 functionResponse)]
	if len(body.Contents) != 3 {
		t.Fatalf("contents len = %d, want 3 (user, model, merged-user); contents=%+v", len(body.Contents), body.Contents)
	}
	last := body.Contents[2]
	if last.Role != core.RoleUser {
		t.Fatalf("merged tool-result role = %q, want user", last.Role)
	}
	var names []string
	for _, p := range last.Parts {
		if p.FunctionResponse != nil {
			names = append(names, p.FunctionResponse.Name)
		}
	}
	if len(names) != 2 || names[0] != "weather" || names[1] != "clock" {
		t.Errorf("merged functionResponse names = %v, want [weather clock]", names)
	}
}
