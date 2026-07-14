package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// captureOpenAIBody runs one Complete against a stub and returns the raw JSON
// keys the provider sent, so we can assert which token-limit field was emitted.
func captureOpenAIBody(t *testing.T, req core.Request) map[string]json.RawMessage {
	t.Helper()
	var body map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","object":"chat.completion","model":"o3-mini","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer srv.Close()

	p, err := New("sk-test-key", srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := p.Complete(context.Background(), req); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	return body
}

// TestComplete_PrefersMaxCompletionTokens verifies #141 regression guard:
// o-series reasoning models reject max_tokens, so when the caller supplies
// max_completion_tokens (or the gateway seam fills max_tokens from it), the
// OpenAI provider must emit max_completion_tokens and NOT max_tokens.
func TestComplete_PrefersMaxCompletionTokens(t *testing.T) {
	t.Run("completion-tokens only emits max_completion_tokens", func(t *testing.T) {
		body := captureOpenAIBody(t, core.Request{
			Model:               "o3-mini",
			Messages:            []core.Message{{Role: core.RoleUser, Content: "hi"}},
			MaxCompletionTokens: intPtr(4096),
		})
		if _, ok := body["max_tokens"]; ok {
			t.Errorf("max_tokens must not be sent for o-series, body=%v", body)
		}
		if got := string(body["max_completion_tokens"]); got != "4096" {
			t.Errorf("max_completion_tokens = %s, want 4096", got)
		}
	})

	t.Run("seam-aliased request (both set) still emits max_completion_tokens only", func(t *testing.T) {
		// Mirrors what NormalizeCompletionTokenLimits produces: both fields set.
		body := captureOpenAIBody(t, core.Request{
			Model:               "o3-mini",
			Messages:            []core.Message{{Role: core.RoleUser, Content: "hi"}},
			MaxTokens:           intPtr(4096),
			MaxCompletionTokens: intPtr(4096),
		})
		if _, ok := body["max_tokens"]; ok {
			t.Errorf("max_tokens must not be sent when max_completion_tokens present, body=%v", body)
		}
		if got := string(body["max_completion_tokens"]); got != "4096" {
			t.Errorf("max_completion_tokens = %s, want 4096", got)
		}
	})

	t.Run("legacy max_tokens only still emits max_tokens", func(t *testing.T) {
		body := captureOpenAIBody(t, core.Request{
			Model:     "gpt-4o",
			Messages:  []core.Message{{Role: core.RoleUser, Content: "hi"}},
			MaxTokens: intPtr(256),
		})
		if got := string(body["max_tokens"]); got != "256" {
			t.Errorf("max_tokens = %s, want 256", got)
		}
		if _, ok := body["max_completion_tokens"]; ok {
			t.Errorf("max_completion_tokens must not appear for a legacy max_tokens request, body=%v", body)
		}
	})
}
