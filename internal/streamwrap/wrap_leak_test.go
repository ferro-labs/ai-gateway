package streamwrap

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/events"
	"github.com/ferro-labs/ai-gateway/internal/metrics"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/providers"
	"go.uber.org/goleak"
)

// TestMain installs a package-wide goroutine-leak check. The Meter goroutine
// MUST exit by the time the consumer-side channel closes — under any
// termination condition (success, provider error, client disconnect,
// consumer-stops-reading).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m, goleak.IgnoreCurrent())
}

// TestMeter_ClientDisconnect_NoGoroutineLeak simulates the production failure
// mode for C3: a slow producer (provider streaming chunks at ~10ms cadence)
// and a consumer (the HTTP handler) that goes away mid-stream because the
// client disconnected. Before the fix, the Meter goroutine blocks forever on
// `out <- chunk`, the upstream goroutine blocks forever on its next send to
// `src`, and the HTTP body never gets closed.
func TestMeter_ClientDisconnect_NoGoroutineLeak(t *testing.T) {
	src := make(chan providers.StreamChunk)

	var producerExited atomic.Bool
	go func() {
		defer close(src)
		defer producerExited.Store(true)
		// Send 50 chunks at 10ms cadence. The consumer cancels at ~25ms, so
		// the producer is still in the middle of streaming when cancel fires.
		for range 50 {
			select {
			case src <- providers.StreamChunk{ID: "chunk"}:
			case <-time.After(200 * time.Millisecond):
				// Defensive: if Meter never reads from src, the producer
				// should NOT itself leak. The C3 fix drains src on cancel,
				// so this branch must never fire in a healthy run.
				t.Errorf("producer write to src blocked > 200ms — Meter is not draining src on cancel")
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())

	var published atomic.Int32
	var publishedSubject atomic.Value
	publishFn := func(_ context.Context, event events.HookEvent) {
		publishedSubject.Store(event.Subject)
		published.Add(1)
	}

	out := Meter(ctx, src, time.Now(), MeterMeta{
		Provider:  "openai",
		Model:     "gpt-4o",
		Catalog:   models.Catalog{},
		PublishFn: publishFn,
		TraceID:   "trace-disconnect",
	})

	// Consume 1 chunk, then disconnect.
	<-out
	time.AfterFunc(15*time.Millisecond, cancel)

	// Wait for Meter to close `out`. Without the fix this hangs forever
	// (caught by the goroutine-leak check, but the explicit timeout gives
	// a clearer failure message).
	done := make(chan struct{})
	go func() {
		for range out { //nolint:revive
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Meter did not close `out` within 2s of ctx cancel — goroutine leak")
	}

	// Producer must have been able to exit (src was drained, not abandoned).
	deadline := time.Now().Add(1 * time.Second)
	for !producerExited.Load() {
		if time.Now().After(deadline) {
			t.Fatal("producer goroutine did not exit within 1s — src was never drained")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if published.Load() != 1 {
		t.Fatalf("publish count = %d, want 1 (failed event for client disconnect)", published.Load())
	}
	if got := publishedSubject.Load(); got != "gateway.request.failed" {
		t.Fatalf("published subject = %v, want gateway.request.failed", got)
	}

	// The client_canceled metric label MUST be incremented so dashboards can
	// distinguish disconnects from real provider errors.
	if v := counterValue(t, metrics.ProviderErrors.WithLabelValues("openai", "client_canceled")); v < 1 {
		t.Fatalf("provider_errors{err=client_canceled} = %v, want >= 1", v)
	}
}

// TestMeter_ConsumerStopsReading_NoLeak guards the inverse: if the consumer
// simply stops reading without cancelling the ctx, Meter should still exit
// once the producer closes `src`. (Sanity check that the new select did not
// accidentally break the natural end-of-stream path.)
func TestMeter_ConsumerStopsReading_NoLeak(t *testing.T) {
	src := feed(
		providers.StreamChunk{ID: "1"},
		providers.StreamChunk{ID: "2"},
		providers.StreamChunk{ID: "3"},
	)

	out := Meter(context.Background(), src, time.Now(), MeterMeta{
		Provider: "openai",
		Model:    "gpt-4o",
		Catalog:  models.Catalog{},
	})

	// Read one, then stop. Meter's per-chunk send is buffered through the
	// unbuffered `out`, so once we stop reading the next send blocks until
	// the consumer drains the rest. The TEST drains the rest below — this
	// case just confirms that close(src) flows through to close(out) without
	// any leak even when the consumer is slow.
	<-out

	done := make(chan struct{})
	go func() {
		for range out { //nolint:revive
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Meter did not close `out` within 2s of src close — natural-EOS leak")
	}
}
