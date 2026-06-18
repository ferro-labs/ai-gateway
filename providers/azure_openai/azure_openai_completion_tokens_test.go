package azureopenai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func intPtr(i int) *int { return &i }

// captureAzureBody runs one Complete against a stub and returns the raw JSON
// keys forwarded to Azure.
func captureAzureBody(t *testing.T, req core.Request) map[string]json.RawMessage {
	t.Helper()
	var body map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","model":"o3-mini","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer srv.Close()

	p, err := New("test-key", srv.URL, "o3-mini", "2024-10-21")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := p.Complete(context.Background(), req); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	return body
}

// TestComplete_DropsMaxTokensForCompletionTokens verifies #141 regression guard:
// Azure o-series deployments reject max_tokens, so when max_completion_tokens is
// present (incl. the gateway-seam-aliased case where both are set) only
// max_completion_tokens is forwarded.
func TestComplete_DropsMaxTokensForCompletionTokens(t *testing.T) {
	t.Run("both set forwards only max_completion_tokens", func(t *testing.T) {
		body := captureAzureBody(t, core.Request{
			Model:               "o3-mini",
			Messages:            []core.Message{{Role: core.RoleUser, Content: "hi"}},
			MaxTokens:           intPtr(4096),
			MaxCompletionTokens: intPtr(4096),
		})
		if _, ok := body["max_tokens"]; ok {
			t.Errorf("max_tokens must not be forwarded when max_completion_tokens set, body=%v", body)
		}
		if got := string(body["max_completion_tokens"]); got != "4096" {
			t.Errorf("max_completion_tokens = %s, want 4096", got)
		}
	})

	t.Run("legacy max_tokens only is preserved", func(t *testing.T) {
		body := captureAzureBody(t, core.Request{
			Model:     "gpt-4o",
			Messages:  []core.Message{{Role: core.RoleUser, Content: "hi"}},
			MaxTokens: intPtr(256),
		})
		if got := string(body["max_tokens"]); got != "256" {
			t.Errorf("max_tokens = %s, want 256", got)
		}
	})
}
