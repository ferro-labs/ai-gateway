package sse

import (
	"bufio"
	"encoding/json"
	"io"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

// BenchmarkSSEWrite_1000Chunks measures the per-chunk write path that runs once
// per token delta. writeChunk takes a concrete *providers.StreamChunk so the
// chunk is not heap-boxed into an `any` on every write; allocs/op should stay
// flat instead of scaling with the chunk count.
func BenchmarkSSEWrite_1000Chunks(b *testing.B) {
	const chunks = 1000

	bw := bufio.NewWriterSize(io.Discard, 4096)
	enc := json.NewEncoder(bw)
	chunk := providers.StreamChunk{
		ID:      "chatcmpl-bench",
		Object:  "chat.completion.chunk",
		Created: 1700000000,
		Model:   "gpt-4o-mini",
		Choices: []providers.StreamChoice{{
			Index: 0,
			Delta: providers.MessageDelta{Role: "assistant", Content: "token"},
		}},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < chunks; j++ {
			if err := writeChunk(bw, enc, &chunk); err != nil {
				b.Fatalf("writeChunk error: %v", err)
			}
		}
		bw.Reset(io.Discard)
	}
}
