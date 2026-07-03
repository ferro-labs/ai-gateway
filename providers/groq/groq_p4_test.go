package groq

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// TestGroqProvider_Complete_PrefersMaxCompletionTokens verifies that when the
// gateway seam populates both token-limit fields, groq forwards only the modern
// max_completion_tokens.
func TestGroqProvider_Complete_PrefersMaxCompletionTokens(t *testing.T) {
	var captured map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{}}`)
	}))
	defer srv.Close()

	p, err := New("test-key", srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := p.Complete(context.Background(), core.Request{
		Model:               "llama-3.1-8b-instant",
		Messages:            []core.Message{{Role: core.RoleUser, Content: "hi"}},
		MaxTokens:           intp(64),
		MaxCompletionTokens: intp(64),
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if _, ok := captured["max_tokens"]; ok {
		t.Errorf("body contains max_tokens, want only max_completion_tokens")
	}
	if _, ok := captured["max_completion_tokens"]; !ok {
		t.Error("body missing max_completion_tokens")
	}
}

// TestNewGroq_RejectsInvalidBaseURL locks in the shared base-URL validation.
func TestNewGroq_RejectsInvalidBaseURL(t *testing.T) {
	if _, err := New("k", "://bad"); err == nil {
		t.Fatal("New accepted an invalid base URL")
	}
}
