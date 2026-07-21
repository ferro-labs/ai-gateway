package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	core "github.com/ferro-labs/ai-gateway/providers/core"
)

// TestOpenAIProvider_CompleteStream_AlwaysRequestsUsageUpstream is the
// provider-side half of the A11/B1 fix: the gateway must always ask OpenAI
// for the terminal usage chunk, regardless of what the client's own
// stream_options said (including an explicit include_usage:false), because
// downstream accounting (cost, metrics, budget plugin) depends on it. Honoring
// the client's opt-out happens one layer up, in internal/streamwrap.Meter —
// never here.
func TestOpenAIProvider_CompleteStream_AlwaysRequestsUsageUpstream(t *testing.T) {
	tests := []struct {
		name                string
		clientStreamOptions *core.StreamOptions
	}{
		{name: "client omitted stream_options", clientStreamOptions: nil},
		{name: "client explicitly set include_usage:false", clientStreamOptions: &core.StreamOptions{IncludeUsage: false}},
		{name: "client explicitly set include_usage:true", clientStreamOptions: &core.StreamOptions{IncludeUsage: true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var capturedBody map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
					t.Errorf("decode captured request body: %v", err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("data: [DONE]\n\n"))
			}))
			defer srv.Close()

			provider, _ := New("sk-test-key", srv.URL)
			ch, err := provider.CompleteStream(context.Background(), core.Request{
				Model:               "gpt-4o",
				Messages:            []core.Message{{Role: "user", Content: "hi"}},
				ClientStreamOptions: tt.clientStreamOptions,
			})
			if err != nil {
				t.Fatalf("CompleteStream() error: %v", err)
			}
			for c := range ch {
				if c.Error != nil {
					t.Fatalf("stream error: %v", c.Error)
				}
			}

			so, ok := capturedBody["stream_options"].(map[string]any)
			if !ok {
				t.Fatalf("upstream request body missing stream_options: %+v", capturedBody)
			}
			if includeUsage, _ := so["include_usage"].(bool); !includeUsage {
				t.Fatalf("upstream stream_options.include_usage = %v, want true unconditionally (client's own choice is %+v)", so["include_usage"], tt.clientStreamOptions)
			}
		})
	}
}
