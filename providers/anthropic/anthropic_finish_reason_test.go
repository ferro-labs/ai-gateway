package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func newTestProvider(t *testing.T, body string) *Provider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	p, err := New("test-key", srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

// TestComplete_NormalizesFinishReason verifies #142: the native Anthropic
// stop_reason is parsed and normalized rather than hardcoded to "stop".
func TestComplete_NormalizesFinishReason(t *testing.T) {
	cases := []struct {
		name       string
		stopReason string
		want       string
	}{
		{"end_turn -> stop", "end_turn", "stop"},
		{"max_tokens -> length", "max_tokens", "length"},
		{"tool_use -> tool_calls", "tool_use", "tool_calls"},
		{"refusal -> content_filter", "refusal", "content_filter"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"id":"x","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"model":"claude","stop_reason":"` + tc.stopReason + `","usage":{"input_tokens":1,"output_tokens":1}}`
			p := newTestProvider(t, body)

			resp, err := p.Complete(context.Background(), core.Request{
				Model:    "claude-3-5-sonnet",
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

// TestCompleteStream_NormalizesFinishReason verifies the streaming message_delta
// stop_reason is parsed and normalized instead of hardcoded to "stop".
func TestCompleteStream_NormalizesFinishReason(t *testing.T) {
	stream := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude","role":"assistant"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":5}}` + "\n\n"

	p := newTestProvider(t, stream)
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "claude-3-5-sonnet",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}

	var final string
	for chunk := range ch {
		if chunk.Error != nil {
			t.Fatalf("stream error: %v", chunk.Error)
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].FinishReason != "" {
			final = chunk.Choices[0].FinishReason
		}
	}
	if final != "length" {
		t.Errorf("stream finish_reason = %q, want length", final)
	}
}
