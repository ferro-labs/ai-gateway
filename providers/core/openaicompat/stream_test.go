package openaicompat

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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

		chunks := collect(StreamSSE(context.Background(), sseBody(body)))

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

	t.Run("accepts data lines with and without the optional space", func(t *testing.T) {
		// The space after "data:" is optional per the SSE spec. An upstream that
		// omits it used to yield no frames at all, so the client received an
		// empty stream reported as a success.
		body := `data:{"id":"a","choices":[{"index":0,"delta":{"content":"Hel"}}]}` + "\n\n" +
			`data: {"id":"a","choices":[{"index":0,"delta":{"content":"lo"}}]}` + "\n\n" +
			"data:[DONE]\n\n"

		chunks := collect(StreamSSE(context.Background(), sseBody(body)))
		if len(chunks) != 2 {
			t.Fatalf("got %d chunks, want 2", len(chunks))
		}
		var content strings.Builder
		for _, c := range chunks {
			for _, ch := range c.Choices {
				content.WriteString(ch.Delta.Content)
			}
		}
		if content.String() != "Hello" {
			t.Errorf("content = %q, want Hello", content.String())
		}
	})

	t.Run("preserves tool_calls and usage deltas", func(t *testing.T) {
		body := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"t1","type":"function","function":{"name":"f"}}]}}]}` + "\n\n" +
			`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}` + "\n\n" +
			"data: [DONE]\n\n"

		chunks := collect(StreamSSE(context.Background(), sseBody(body)))
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

// TestStreamSSE_MidStreamError pins the mid-stream failure contract for every
// provider that streams through PostStream: an upstream that fails once the 200
// headers are out reports it as a "data:" frame carrying an error envelope. The
// consumer must observe that failure — a stream cut short must never read as a
// complete answer — and must see nothing after it.
func TestStreamSSE_MidStreamError(t *testing.T) {
	t.Run("surfaces the upstream error and ends the stream", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, strings.Join([]string{
				`data: {"id":"a","choices":[{"index":0,"delta":{"content":"Hel"}}]}`,
				`data: {"error":{"message":"upstream exploded","type":"server_error"}}`,
				`data: [DONE]`,
			}, "\n\n")+"\n\n")
		}))
		defer srv.Close()

		ch, err := PostStream(context.Background(), ChatParams{
			HTTPClient: srv.Client(),
			URL:        srv.URL,
			Headers:    map[string]string{"Content-Type": "application/json"},
			Provider:   "test",
			Label:      "test",
		}, core.Request{Model: "m", Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}}})
		if err != nil {
			t.Fatalf("PostStream: %v", err)
		}

		chunks := collect(ch)
		if len(chunks) != 2 {
			t.Fatalf("got %d chunks, want 2 (content, then the error)", len(chunks))
		}
		if chunks[0].Error != nil {
			t.Errorf("content chunk carries an error: %v", chunks[0].Error)
		}
		last := chunks[1]
		if last.Error == nil {
			t.Fatal("mid-stream error frame delivered as a successful chunk — the truncated answer reads as complete")
		}
		if !strings.Contains(last.Error.Error(), "upstream exploded") ||
			!strings.Contains(last.Error.Error(), "server_error") {
			t.Errorf("error = %v, want the upstream message and type", last.Error)
		}
	})

	t.Run("only a populated message is a failure", func(t *testing.T) {
		tests := []struct {
			name    string
			frame   string
			wantErr string // empty means the frame must decode as a healthy chunk
		}{
			{
				name:    "message and type",
				frame:   `{"error":{"message":"boom","type":"server_error"}}`,
				wantErr: "stream error (server_error): boom",
			},
			{
				name:    "message without type",
				frame:   `{"error":{"message":"boom"}}`,
				wantErr: "stream error: boom",
			},
			{
				name:  "null error alongside content",
				frame: `{"id":"a","error":null,"choices":[{"index":0,"delta":{"content":"lo"}}]}`,
			},
			{
				name:  "empty envelope alongside content",
				frame: `{"id":"a","error":{},"choices":[{"index":0,"delta":{"content":"lo"}}]}`,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				chunk, err := DecodeStreamChunk([]byte(tt.frame))
				if err != nil {
					t.Fatalf("DecodeStreamChunk: %v", err)
				}
				switch {
				case tt.wantErr == "" && chunk.Error != nil:
					t.Fatalf("Error = %v, want nil", chunk.Error)
				case tt.wantErr == "":
					if chunk.Choices[0].Delta.Content != "lo" {
						t.Errorf("content = %q, want %q", chunk.Choices[0].Delta.Content, "lo")
					}
				case chunk.Error == nil:
					t.Fatalf("Error = nil, want %q", tt.wantErr)
				case chunk.Error.Error() != tt.wantErr:
					t.Errorf("Error = %q, want %q", chunk.Error, tt.wantErr)
				}
			})
		}
	})
}
