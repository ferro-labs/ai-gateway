package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	core "github.com/ferro-labs/ai-gateway/providers/core"
)

// TestCompleteStream_MidStreamError verifies that an error OpenAI reports once
// the 200 headers are already out reaches the consumer as a failed chunk. The
// frame carries no choices, so decoding only the chunk fields would deliver a
// truncated answer as a complete one; the stream must also stop there.
func TestCompleteStream_MidStreamError(t *testing.T) {
	tests := []struct {
		name     string
		frame    string
		wantErr  string // empty means the frame must decode as a healthy chunk
		wantText string
	}{
		{
			name:     "error envelope with type",
			frame:    `data: {"error":{"message":"upstream exploded","type":"server_error"}}`,
			wantErr:  "stream error (server_error): upstream exploded",
			wantText: "Hel",
		},
		{
			name:     "error envelope without type",
			frame:    `data: {"error":{"message":"upstream exploded"}}`,
			wantErr:  "stream error: upstream exploded",
			wantText: "Hel",
		},
		{
			name:     "null error is not a failure",
			frame:    `data: {"id":"a","error":null,"choices":[{"index":0,"delta":{"content":"lo"}}]}`,
			wantText: "Hello",
		},
		{
			name:     "empty envelope is not a failure",
			frame:    `data: {"id":"a","error":{},"choices":[{"index":0,"delta":{"content":"lo"}}]}`,
			wantText: "Hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, strings.Join([]string{
					`data: {"id":"a","choices":[{"index":0,"delta":{"content":"Hel"}}]}`,
					tt.frame,
					`data: [DONE]`,
				}, "\n\n")+"\n\n")
			}))
			defer srv.Close()

			provider, _ := New("sk-test-key", srv.URL)
			ch, err := provider.CompleteStream(context.Background(), core.Request{
				Model:    "gpt-4o",
				Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
			})
			if err != nil {
				t.Fatalf("CompleteStream() error: %v", err)
			}

			var (
				content   strings.Builder
				streamErr error
			)
			for c := range ch {
				if c.Error != nil {
					streamErr = c.Error
				}
				for _, choice := range c.Choices {
					content.WriteString(choice.Delta.Content)
				}
			}

			switch {
			case tt.wantErr == "" && streamErr != nil:
				t.Fatalf("stream error = %v, want none", streamErr)
			case tt.wantErr == "":
			case streamErr == nil:
				t.Fatalf("mid-stream error delivered as a successful, truncated answer (content %q)", content.String())
			case streamErr.Error() != tt.wantErr:
				t.Errorf("stream error = %q, want %q", streamErr, tt.wantErr)
			}
			if content.String() != tt.wantText {
				t.Errorf("content = %q, want %q", content.String(), tt.wantText)
			}
		})
	}
}
