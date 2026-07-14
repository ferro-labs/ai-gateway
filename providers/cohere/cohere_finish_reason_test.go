package cohere

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// TestComplete_NormalizesFinishReason verifies #142: native Cohere finish
// reasons are mapped to the OpenAI-canonical vocabulary.
func TestComplete_NormalizesFinishReason(t *testing.T) {
	cases := []struct {
		name   string
		native string
		want   string
	}{
		{"COMPLETE -> stop", "COMPLETE", "stop"},
		{"MAX_TOKENS -> length", "MAX_TOKENS", "length"},
		{"TOOL_CALL -> tool_calls", "TOOL_CALL", "tool_calls"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"x","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]},"finish_reason":"`+tc.native+`","usage":{"tokens":{"input_tokens":1,"output_tokens":1}}}`)
			}))
			defer srv.Close()

			p, err := New("test-key", srv.URL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			resp, err := p.Complete(context.Background(), core.Request{
				Model:    "command-r",
				Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
			})
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if got := resp.Choices[0].FinishReason; got != tc.want {
				t.Errorf("finish_reason = %q, want %q", got, tc.want)
			}
		})
	}
}
