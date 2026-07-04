package openaicompat

import (
	"io"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

type stringReadCloser struct{ *strings.Reader }

func (stringReadCloser) Close() error { return nil }

func sseBody(s string) io.ReadCloser { return stringReadCloser{strings.NewReader(s)} }

func collect(ch <-chan core.StreamChunk) []core.StreamChunk {
	var out []core.StreamChunk
	for c := range ch {
		out = append(out, c)
	}
	return out
}

// TestStreamSSE_Contract pins the documented behavior of the shared SSE reader,
// which is the single source of truth for 23 providers' streaming. In
// particular it guards the decode-error policy: a malformed "data:" frame is
// skipped (not fatal) so benign/garbled frames don't abort an otherwise healthy
// stream — mirroring the OpenAI client SDKs.
func TestStreamSSE_Contract(t *testing.T) {
	t.Run("delivers valid frames, skips malformed, honors [DONE]", func(t *testing.T) {
		body := strings.Join([]string{
			`data: {"id":"a","choices":[{"index":0,"delta":{"content":"Hel"}}]}`,
			`: keep-alive comment`,  // non-"data:" line -> skipped
			`data: {not valid json`, // malformed -> skipped, NOT fatal
			`data: {"id":"a","choices":[{"index":0,"delta":{"content":"lo"}}]}`,
			`data: [DONE]`, // terminates
			`data: {"id":"a","choices":[{"delta":{"content":"AFTER"}}]}`, // never read
		}, "\n\n") + "\n\n"

		chunks := collect(StreamSSE(sseBody(body)))

		if len(chunks) != 2 {
			t.Fatalf("got %d chunks, want 2 (malformed skipped, [DONE] stops)", len(chunks))
		}
		var content strings.Builder
		for _, c := range chunks {
			if c.Error != nil {
				t.Fatalf("unexpected error chunk: %v", c.Error)
			}
			for _, ch := range c.Choices {
				content.WriteString(ch.Delta.Content)
			}
		}
		if content.String() != "Hello" {
			t.Errorf("content = %q, want Hello (no AFTER past [DONE])", content.String())
		}
	})

	t.Run("preserves tool_calls and usage deltas", func(t *testing.T) {
		body := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"t1","type":"function","function":{"name":"f"}}]}}]}` + "\n\n" +
			`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}` + "\n\n" +
			"data: [DONE]\n\n"

		chunks := collect(StreamSSE(sseBody(body)))
		if len(chunks) != 2 {
			t.Fatalf("got %d chunks, want 2", len(chunks))
		}
		tc := chunks[0].Choices[0].Delta.ToolCalls
		if len(tc) != 1 || tc[0].Index == nil || *tc[0].Index != 0 || tc[0].ID != "t1" || tc[0].Function.Name != "f" {
			t.Errorf("tool_call delta not preserved: %+v", tc)
		}
		if chunks[1].Usage == nil || chunks[1].Usage.TotalTokens != 5 {
			t.Errorf("usage not preserved: %+v", chunks[1].Usage)
		}
		if chunks[1].Choices[0].FinishReason != "tool_calls" {
			t.Errorf("finish_reason = %q, want tool_calls", chunks[1].Choices[0].FinishReason)
		}
	})
}
