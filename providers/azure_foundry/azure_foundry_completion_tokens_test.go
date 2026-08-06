package azurefoundry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// captureFoundryBody drives one surface against a stub and returns the raw JSON
// keys forwarded to Foundry. send is Complete or CompleteStream; both must agree,
// which is the drift this guard exists to catch.
func captureFoundryBody(t *testing.T, req core.Request, send func(context.Context, *Provider, core.Request) error) map[string]json.RawMessage {
	t.Helper()
	var body map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","model":"o3-mini","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer srv.Close()

	p, err := New(testAPIKey, srv.URL, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := send(context.Background(), p, req); err != nil {
		t.Fatalf("send: %v", err)
	}
	return body
}

func foundryComplete(ctx context.Context, p *Provider, req core.Request) error {
	_, err := p.Complete(ctx, req)
	return err
}

func foundryStream(ctx context.Context, p *Provider, req core.Request) error {
	ch, err := p.CompleteStream(ctx, req)
	if err != nil {
		return err
	}
	for range ch { //nolint:revive // drain so the producer goroutine finishes before the stub closes
	}
	return nil
}

// TestCompletionTokensPreferredOverMaxTokens pins the completion-ceiling field
// Foundry's GA /openai/v1 route is sent. That route is the OpenAI surface, so an
// o-series or gpt-5 deployment rejects max_tokens with a 400 — and the gateway
// seam fills max_tokens from max_completion_tokens, so both are set by the time
// the provider builds its body. Forwarding both broke that whole model class
// through this provider while working everywhere else.
func TestCompletionTokensPreferredOverMaxTokens(t *testing.T) {
	surfaces := map[string]func(context.Context, *Provider, core.Request) error{
		"Complete":       foundryComplete,
		"CompleteStream": foundryStream,
	}
	limit := 4096

	for name, send := range surfaces {
		t.Run(name+"/both set forwards only max_completion_tokens", func(t *testing.T) {
			body := captureFoundryBody(t, core.Request{
				Model:               "o3-mini",
				Messages:            []core.Message{{Role: core.RoleUser, Content: "hi"}},
				MaxTokens:           &limit,
				MaxCompletionTokens: &limit,
			}, send)
			if _, ok := body["max_tokens"]; ok {
				t.Errorf("max_tokens forwarded alongside max_completion_tokens; o-series rejects it. body=%v", body)
			}
			if got := string(body["max_completion_tokens"]); got != "4096" {
				t.Errorf("max_completion_tokens = %s, want 4096", got)
			}
		})

		t.Run(name+"/legacy max_tokens only is promoted to max_completion_tokens", func(t *testing.T) {
			// A max_tokens-only request must still reach an o-series / GPT-5
			// deployment, which rejects max_tokens — so it is promoted to the
			// modern field (same ceiling) and max_tokens is dropped.
			body := captureFoundryBody(t, core.Request{
				Model:     "gpt-4o",
				Messages:  []core.Message{{Role: core.RoleUser, Content: "hi"}},
				MaxTokens: &limit,
			}, send)
			if _, ok := body["max_tokens"]; ok {
				t.Errorf("max_tokens must not be forwarded (reasoning deployments reject it). body=%v", body)
			}
			if got := string(body["max_completion_tokens"]); got != "4096" {
				t.Errorf("max_completion_tokens = %s, want 4096 (promoted from max_tokens)", got)
			}
		})
	}
}
