package aigateway

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/providers"
)

// blockingProvider blocks in Complete until released, so a test can hold in-flight
// slots open and observe the concurrency cap.
type blockingProvider struct {
	mockProvider
	inFlight atomic.Int32
	peak     atomic.Int32
	release  chan struct{}
}

func newBlockingProvider(name string) *blockingProvider {
	return &blockingProvider{
		mockProvider: mockProvider{name: name, models: []string{"gpt-4o"}},
		release:      make(chan struct{}),
	}
}

func (b *blockingProvider) Complete(ctx context.Context, _ providers.Request) (*providers.Response, error) {
	n := b.inFlight.Add(1)
	for {
		peak := b.peak.Load()
		if n <= peak || b.peak.CompareAndSwap(peak, n) {
			break
		}
	}
	defer b.inFlight.Add(-1)

	select {
	case <-b.release:
		return &providers.Response{ID: "ok"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ── Limiter admission control (issue #248 acceptance criteria) ────────────────

func TestProviderLimiter_UnderLimitDoesNotBlock(t *testing.T) {
	lim := newProviderLimiter(2, 1)
	for range 2 {
		if err := lim.acquire(context.Background()); err != nil {
			t.Fatalf("acquire under the limit must not block or fail: %v", err)
		}
	}
}

func TestProviderLimiter_QueueFullShedsImmediately(t *testing.T) {
	lim := newProviderLimiter(1, 1) // 1 in flight, 1 may wait

	if err := lim.acquire(context.Background()); err != nil { // takes the only slot
		t.Fatalf("first acquire: %v", err)
	}

	// Occupy the single queue position.
	queued := make(chan struct{})
	go func() {
		close(queued)
		_ = lim.acquire(context.Background())
	}()
	<-queued
	// Give the queued goroutine a moment to register as waiting.
	waitFor(t, func() bool { return lim.waiting.Load() == 1 })

	// The queue is now full: the next caller must be shed, not blocked.
	err := lim.acquire(context.Background())
	if !errors.Is(err, providers.ErrProviderSaturated) {
		t.Errorf("acquire beyond queue_size = %v, want ErrProviderSaturated (shed, never block)", err)
	}
}

func TestProviderLimiter_CancelWhileWaitingHoldsNoSlot(t *testing.T) {
	lim := newProviderLimiter(1, 10)
	if err := lim.acquire(context.Background()); err != nil { // hold the only slot
		t.Fatalf("first acquire: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		waitFor(t, func() bool { return lim.waiting.Load() == 1 })
		cancel()
	}()

	if err := lim.acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("acquire = %v, want context.Canceled", err)
	}
	// The cancelled caller must not have left a slot or a queue seat behind.
	if got := lim.waiting.Load(); got != 0 {
		t.Errorf("waiting = %d, want 0: a cancelled request must not occupy a queue seat", got)
	}
	lim.release() // the original holder
	if err := lim.acquire(context.Background()); err != nil {
		t.Errorf("the slot must be free again after the holder released it: %v", err)
	}
}

// ── The decorator ─────────────────────────────────────────────────────────────

func TestLimitedProvider_CapsConcurrentCompletes(t *testing.T) {
	const maxConcurrency = 2
	inner := newBlockingProvider("capped")
	p := decorateProvider("capped", inner, nil, newProviderLimiter(maxConcurrency, 100))

	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = p.Complete(context.Background(), providers.Request{Model: "gpt-4o"})
		}()
	}
	// Let the admitted callers reach the provider, then release everyone.
	waitFor(t, func() bool { return inner.inFlight.Load() == maxConcurrency })
	close(inner.release)
	wg.Wait()

	if peak := inner.peak.Load(); peak > maxConcurrency {
		t.Errorf("peak in-flight = %d, want <= %d: the concurrency cap was not enforced", peak, maxConcurrency)
	}
}

// TestLimitedProvider_DoesNotForgeCapabilities is the regression guard for the
// rejected worker-pool design, which declared every optional interface
// unconditionally. A wrapper that advertises capabilities its inner provider
// lacks silently corrupts capability detection — model indexing would route an
// image request to a chat-only provider, and a native-wire provider would lose its
// NonOpenAIWire marker and become wrongly eligible for OpenAI-wire pass-through.
// The decorator must expose exactly the base Provider surface, like cbProvider.
func TestLimitedProvider_DoesNotForgeCapabilities(t *testing.T) {
	chatOnly := &mockProvider{name: "chat-only", models: []string{"gpt-4o"}}
	decorated := decorateProvider("chat-only", chatOnly, nil, newProviderLimiter(1, 1))

	if _, ok := decorated.(providers.EmbeddingProvider); ok {
		t.Error("decorated chat-only provider must not satisfy EmbeddingProvider")
	}
	if _, ok := decorated.(providers.ImageProvider); ok {
		t.Error("decorated chat-only provider must not satisfy ImageProvider")
	}
	if _, ok := decorated.(providers.DiscoveryProvider); ok {
		t.Error("decorated chat-only provider must not satisfy DiscoveryProvider")
	}
	if _, ok := decorated.(providers.ProxiableProvider); ok {
		t.Error("decorated chat-only provider must not satisfy ProxiableProvider")
	}
}

// streamHoldProvider returns a stream that stays open until released, so a test can
// observe whether the concurrency slot is held for the stream's whole lifetime.
type streamHoldProvider struct {
	mockProvider
	release chan struct{}
}

func (s *streamHoldProvider) CompleteStream(ctx context.Context, _ providers.Request) (<-chan providers.StreamChunk, error) {
	ch := make(chan providers.StreamChunk)
	go func() {
		defer close(ch)
		select {
		case <-s.release:
		case <-ctx.Done():
		}
	}()
	return ch, nil
}

// TestLimitedProvider_StreamHoldsSlotForWholeStream is the regression guard for the
// rejected worker-pool design, which released the slot as soon as response headers
// arrived. An upstream SSE connection is occupied until its last chunk, so releasing
// at setup time let unbounded streams run concurrently and defeated the cap entirely.
func TestLimitedProvider_StreamHoldsSlotForWholeStream(t *testing.T) {
	inner := &streamHoldProvider{
		mockProvider: mockProvider{name: "streamer", models: []string{"gpt-4o"}},
		release:      make(chan struct{}),
	}
	lim := newProviderLimiter(1, 0) // exactly one in-flight slot
	sp, ok := decorateProvider("streamer", inner, nil, lim).(providers.StreamProvider)
	if !ok {
		t.Fatal("decorated stream provider must satisfy StreamProvider")
	}

	ch, err := sp.CompleteStream(context.Background(), providers.Request{Model: "gpt-4o"})
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}

	// The stream is still open, so its slot must still be held: a second caller
	// must not be admitted.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if err := lim.acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("acquire during an open stream = %v; the slot must stay held for the stream's whole "+
			"lifetime, not be released at setup", err)
	}

	// End the stream and drain it; the slot must then come back.
	close(inner.release)
	for range ch { //nolint:revive // draining the stream to completion is the point
	}
	waitFor(t, func() bool {
		select {
		case lim.slots <- struct{}{}:
			<-lim.slots
			return true
		default:
			return false
		}
	})
}

// TestDecorateProvider_CircuitBreakerIsOutermost proves an OPEN circuit fails fast
// without ever consuming an in-flight slot or a queue seat.
func TestDecorateProvider_CircuitBreakerIsOutermost(t *testing.T) {
	inner := newBlockingProvider("tripped")
	lim := newProviderLimiter(1, 1)
	cb := circuitbreaker.New(1, 1, 1, time.Minute)
	cb.RecordFailure() // threshold 1 → open

	p := decorateProvider("tripped", inner, cb, lim)

	_, err := p.Complete(context.Background(), providers.Request{Model: "gpt-4o"})
	if !errors.Is(err, circuitbreaker.ErrCircuitOpen) {
		t.Fatalf("err = %v, want ErrCircuitOpen", err)
	}
	// The limiter must be untouched: the open circuit short-circuited first.
	if err := lim.acquire(context.Background()); err != nil {
		t.Errorf("an open circuit must not consume a concurrency slot: %v", err)
	}
	if inner.inFlight.Load() != 0 {
		t.Error("an open circuit must never reach the provider")
	}
}

// waitFor polls cond until true, failing the test if it never becomes true. It
// avoids fixed sleeps, which make concurrency tests flaky.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}
