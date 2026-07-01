package wordfilter

import (
	"context"
	"testing"

	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

// BenchmarkWordFilter_Execute exercises the case-insensitive scan over several
// blocked words and messages. With blocked words pre-lowered in Init, the
// per-word strings.ToLower allocations are gone from the hot path.
func BenchmarkWordFilter_Execute(b *testing.B) {
	f := &WordFilter{}
	if err := f.Init(map[string]any{
		"blocked_words":  []any{"Password", "Secret", "ApiKey", "Token", "Credential"},
		"case_sensitive": false,
	}); err != nil {
		b.Fatalf("Init failed: %v", err)
	}

	req := &providers.Request{
		Model: "gpt-4",
		Messages: []providers.Message{
			{Role: "system", Content: "You are a helpful assistant that answers concisely."},
			{Role: "user", Content: "Please summarize the quarterly financial report for the leadership team."},
			{Role: "assistant", Content: "Sure, here is a clean overview without any flagged content."},
		},
	}
	pctx := plugin.NewContext(req)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := f.Execute(ctx, pctx); err != nil {
			b.Fatalf("Execute error: %v", err)
		}
	}
}
