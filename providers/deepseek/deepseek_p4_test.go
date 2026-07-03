package deepseek

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// TestDeepSeekProvider_Complete_NormalizesFinishReason verifies the hand-rolled
// non-streaming decode normalizes finish reasons to the canonical OpenAI
// vocabulary, matching the shared streaming path.
func TestDeepSeekProvider_Complete_NormalizesFinishReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","model":"deepseek-chat","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"model_length"}],"usage":{"total_tokens":1}}`))
	}))
	defer srv.Close()

	p, err := New("test-key", srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "deepseek-chat",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Choices[0].FinishReason != core.FinishReasonLength {
		t.Errorf("finish_reason = %q, want length (normalized from model_length)", resp.Choices[0].FinishReason)
	}
}
