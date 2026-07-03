package azureopenai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// TestComplete_DecodesResponseWithProvider verifies the chat response is decoded
// (id/model/content/usage) and now carries the provider name (previously
// dropped).
func TestComplete_DecodesResponseWithProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	}))
	defer srv.Close()

	p, err := New("test-key", srv.URL, "gpt-4o", "2024-10-21")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "gpt-4o",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Provider != Name {
		t.Errorf("Provider = %q, want %q", resp.Provider, Name)
	}
	if resp.ID != "chatcmpl-1" || resp.Model != "gpt-4o" {
		t.Errorf("id/model = %q/%q", resp.ID, resp.Model)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "hello" {
		t.Errorf("choices = %+v", resp.Choices)
	}
	if resp.Usage.PromptTokens != 3 || resp.Usage.CompletionTokens != 2 || resp.Usage.TotalTokens != 5 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestComplete_ErrorPathReturnsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"rate limited"}}`)
	}))
	defer srv.Close()

	p, err := New("test-key", srv.URL, "gpt-4o", "2024-10-21")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = p.Complete(context.Background(), core.Request{
		Model:    "gpt-4o",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for 429")
	}
	if !strings.Contains(err.Error(), "rate limited") || !strings.Contains(err.Error(), "429") {
		t.Fatalf("error = %v, want status + message", err)
	}
}

func TestNew_RejectsInvalidBaseURL(t *testing.T) {
	if _, err := New("k", "://bad", "dep", ""); err == nil {
		t.Fatal("New accepted an invalid base URL")
	}
}
