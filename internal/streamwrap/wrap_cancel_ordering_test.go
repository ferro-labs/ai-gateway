package streamwrap

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/events"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/providers"
)

// meterSubjectsWithCancelledCtx runs Meter over src with a request context that
// is already cancelled — the HTTP handler returning in the same instant the
// upstream finishes — drains out, and returns the published subjects.
func meterSubjectsWithCancelledCtx(src <-chan providers.StreamChunk) []string {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var mu sync.Mutex
	var subjects []string
	out := Meter(ctx, src, time.Now(), MeterMeta{
		Provider:    "openai",
		Model:       "gpt-4o",
		MetricModel: "gpt-4o",
		Catalog:     models.Catalog{},
		PublishFn: func(_ context.Context, event events.HookEvent) {
			mu.Lock()
			subjects = append(subjects, event.Subject)
			mu.Unlock()
		},
	})
	for range out { //nolint:revive // empty-block: intentionally draining the stream to completion
	}

	mu.Lock()
	defer mu.Unlock()
	return append([]string(nil), subjects...)
}

// A provider that has already closed src did the work and billed for it, so
// the record must say completed even when the caller hangs up in the same
// instant. Both channels are ready at the top of the loop; a plain select
// picks between them at random, which logged a finished stream as a client
// cancellation about half the time. Fifty iterations make that visible.
//
// The forward site has no equivalent guarantee on purpose: out is unbuffered,
// so a consumer that has not reached its receive yet is indistinguishable
// from one that is gone, and a test asserting otherwise can only be flaky.
func TestMeter_FinishedUpstreamOutranksConsumerCancellation(t *testing.T) {
	for i := range 50 {
		got := meterSubjectsWithCancelledCtx(feed())
		if len(got) != 1 || got[0] != "gateway.request.completed" {
			t.Fatalf("iteration %d: subjects = %v, want [gateway.request.completed]", i, got)
		}
	}
}
