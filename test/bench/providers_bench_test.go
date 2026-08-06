// Package bench holds cross-provider microbenchmarks.
//
// BenchmarkProviderComplete measures the per-provider cost of one chat
// completion — marshalling core.Request to the provider's wire format and
// decoding its native response back to core.Response — with NO network and NO
// socket. Each provider is built through its own ProviderEntry and its shared
// HTTP client's transport is swapped (via test/providerstub) for a mock that
// returns that provider's real testdata/chat.response.json in memory. So the
// numbers isolate the translation hot path rather than a loopback round-trip.
//
//	go test -bench=. -benchmem ./test/bench/     # or: make bench
//	go test -bench=Provider/openai -benchmem ./test/bench/
package bench

import (
	"context"
	"testing"

	"github.com/ferro-labs/ai-gateway/test/providerstub"
)

// BenchmarkProviderComplete runs one sub-benchmark per stubbable provider. The
// set is guarded against drift by test/providerstub's own coverage test.
func BenchmarkProviderComplete(b *testing.B) {
	ctx := context.Background()
	for id, fx := range providerstub.Fixtures() {
		b.Run(id, func(b *testing.B) {
			restore := providerstub.InstallResponse(id, 200, providerstub.LoadChatResponse(b, id))
			defer restore()

			p := providerstub.Build(b, id)
			req := providerstub.ChatRequest(fx.Model)

			// One warm call surfaces a broken fixture (or a provider that does not
			// use the shared client) as a clear failure rather than a bad timing.
			if _, err := p.Complete(ctx, req); err != nil {
				b.Fatalf("%s Complete warmup: %v", id, err)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := p.Complete(ctx, req); err != nil {
					b.Fatalf("%s Complete: %v", id, err)
				}
			}
		})
	}
}
