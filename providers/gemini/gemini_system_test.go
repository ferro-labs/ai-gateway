package gemini

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// capturedGeminiBody mirrors the request fields relevant to #144: the dedicated
// systemInstruction channel and the conversation contents.
type capturedGeminiBody struct {
	SystemInstruction *struct {
		Role  string `json:"role"`
		Parts []struct {
			Text string `json:"text"`
		} `json:"parts"`
	} `json:"systemInstruction"`
	Contents []struct {
		Role  string `json:"role"`
		Parts []struct {
			Text string `json:"text"`
		} `json:"parts"`
	} `json:"contents"`
}

func captureComplete(t *testing.T, messages []core.Message) capturedGeminiBody {
	t.Helper()
	var captured capturedGeminiBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &captured); err != nil {
			t.Errorf("unmarshal request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"ok"}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`)
	}))
	defer srv.Close()

	p, err := New("test-key", srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := p.Complete(context.Background(), core.Request{
		Model:    "gemini-1.5-pro",
		Messages: messages,
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	return captured
}

// TestSystemPrompt_DeliveredViaSystemInstruction verifies the system prompt is
// routed through Gemini's dedicated systemInstruction field and is NOT smuggled
// into the user turn (#144).
func TestSystemPrompt_DeliveredViaSystemInstruction(t *testing.T) {
	got := captureComplete(t, []core.Message{
		{Role: core.RoleSystem, Content: "You are a helpful assistant."},
		{Role: core.RoleUser, Content: "Hello"},
	})

	if got.SystemInstruction == nil {
		t.Fatalf("systemInstruction missing from request body")
	}
	if len(got.SystemInstruction.Parts) != 1 || got.SystemInstruction.Parts[0].Text != "You are a helpful assistant." {
		t.Errorf("systemInstruction = %+v, want single part with system text", got.SystemInstruction.Parts)
	}
	if len(got.Contents) != 1 || got.Contents[0].Role != "user" {
		t.Fatalf("contents = %+v, want single user turn", got.Contents)
	}
	if txt := got.Contents[0].Parts[0].Text; strings.Contains(txt, "helpful assistant") {
		t.Errorf("system text leaked into user turn: %q", txt)
	}
	if txt := got.Contents[0].Parts[0].Text; txt != "Hello" {
		t.Errorf("user turn = %q, want %q", txt, "Hello")
	}
}

// TestSystemPrompt_PreservedWhenNoUserFollows is the regression for the silent
// drop: a trailing system message (no user turn after it) must still be delivered.
func TestSystemPrompt_PreservedWhenNoUserFollows(t *testing.T) {
	got := captureComplete(t, []core.Message{
		{Role: core.RoleUser, Content: "Hi"},
		{Role: core.RoleAssistant, Content: "Hello!"},
		{Role: core.RoleSystem, Content: "Always answer in French."},
	})

	if got.SystemInstruction == nil {
		t.Fatalf("systemInstruction dropped when system message is last")
	}
	if got.SystemInstruction.Parts[0].Text != "Always answer in French." {
		t.Errorf("systemInstruction = %q, want %q", got.SystemInstruction.Parts[0].Text, "Always answer in French.")
	}
}

// TestSystemPrompt_MultipleConcatenated verifies multiple system messages are
// joined with newlines regardless of turn order.
func TestSystemPrompt_MultipleConcatenated(t *testing.T) {
	got := captureComplete(t, []core.Message{
		{Role: core.RoleSystem, Content: "Rule one."},
		{Role: core.RoleUser, Content: "Hi"},
		{Role: core.RoleSystem, Content: "Rule two."},
	})

	if got.SystemInstruction == nil {
		t.Fatalf("systemInstruction missing")
	}
	if want := "Rule one.\nRule two."; got.SystemInstruction.Parts[0].Text != want {
		t.Errorf("systemInstruction = %q, want %q", got.SystemInstruction.Parts[0].Text, want)
	}
}

// TestSystemPrompt_AbsentWhenNoSystemMessage verifies omitempty: a request with
// no system message must not emit a systemInstruction field.
func TestSystemPrompt_AbsentWhenNoSystemMessage(t *testing.T) {
	got := captureComplete(t, []core.Message{
		{Role: core.RoleUser, Content: "Hello"},
	})

	if got.SystemInstruction != nil {
		t.Errorf("systemInstruction should be absent, got %+v", got.SystemInstruction)
	}
}
