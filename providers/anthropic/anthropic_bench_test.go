package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// BenchmarkAnthropicComplete_Decode exercises the non-stream success path,
// which now decodes straight off the response body via json.NewDecoder instead
// of io.ReadAll + json.Unmarshal, avoiding an extra full-body copy per request.
func BenchmarkAnthropicComplete_Decode(b *testing.B) {
	const respBody = `{"id":"msg_01ABC","type":"message","role":"assistant",` +
		`"content":[{"type":"text","text":"The quick brown fox jumps over the lazy dog. ` +
		`Here is a reasonably sized completion body so the decode path has real work to do."}],` +
		`"model":"claude-3-haiku-20240307","stop_reason":"end_turn",` +
		`"usage":{"input_tokens":42,"output_tokens":128}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	p, _ := New("sk-test-key", srv.URL)
	ctx := context.Background()
	req := core.Request{
		Model:    "claude-3-haiku-20240307",
		Messages: []core.Message{{Role: core.RoleUser, Content: "Say something."}},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, err := p.Complete(ctx, req)
		if err != nil {
			b.Fatalf("Complete() error: %v", err)
		}
		if resp == nil || len(resp.Choices) == 0 {
			b.Fatal("Complete() returned empty response")
		}
	}
}
