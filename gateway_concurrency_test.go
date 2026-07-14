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

// block occupies an in-flight slot until release fires or ctx is cancelled,
// tracking peak concurrency. Shared by Complete, Embed, and GenerateImage so
// every surface can prove the target's limiter caps it the same way.
func (b *blockingProvider) block(ctx context.Context) error {
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
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *blockingProvider) Complete(ctx context.Context, _ providers.Request) (*providers.Response, error) {
	if err := b.block(ctx); err != nil {
		return nil, err
	}
	return &providers.Response{ID: "ok"}, nil
}

func (b *blockingProvider) Embed(ctx context.Context, _ providers.EmbeddingRequest) (*providers.EmbeddingResponse, error) {
	if err := b.block(ctx); err != nil {
		return nil, err
	}
	return &providers.EmbeddingResponse{}, nil
}

func (b *blockingProvider) GenerateImage(ctx context.Context, _ providers.ImageRequest) (*providers.ImageResponse, error) {
	if err := b.block(ctx); err != nil {
		return nil, err
	}
	return &providers.ImageResponse{}, nil
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
	queuedErr := make(chan error, 1)
	go func() { queuedErr <- lim.acquire(context.Background()) }()
	waitFor(t, func() bool { return lim.waiting.Load() == 1 })

	// The queue is now full: the next caller must be shed, not blocked.
	err := lim.acquire(context.Background())
	if !errors.Is(err, providers.ErrProviderSaturated) {
		t.Errorf("acquire beyond queue_size = %v, want ErrProviderSaturated (shed, never block)", err)
	}

	// Free the held slot so the queued acquire can complete, then join it.
	lim.release()
	if err := <-queuedErr; err != nil {
		t.Errorf("queued acquire = %v, want nil once a slot freed", err)
	}
}

func TestProviderLimiter_CancelWhileWaitingHoldsNoSlot(t *testing.T) {
	lim := newProviderLimiter(1, 10)
	if err := lim.acquire(context.Background()); err != nil { // hold the only slot
		t.Fatalf("first acquire: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	acquireErr := make(chan error, 1)
	go func() { acquireErr <- lim.acquire(ctx) }()

	waitFor(t, func() bool { return lim.waiting.Load() == 1 })
	cancel()

	if err := <-acquireErr; !errors.Is(err, context.Canceled) {
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

// ── Embed / GenerateImage concurrency wiring (issue #347) ─────────────────────
//
// Embed and GenerateImage resolve their provider through the capability-specific
// model index (findEmbeddingProviderByModelLocked / findImageProviderByModelLocked),
// not through decorateProvider, so these prove the target's limiter is applied at
// the call site instead.

// multiModalCalls exercises Embed and GenerateImage identically against the same
// target so both surfaces are proven to share the concurrency wiring.
var multiModalCalls = []struct {
	name string
	call func(ctx context.Context, gw *Gateway) error
}{
	{
		name: "embed",
		call: func(ctx context.Context, gw *Gateway) error {
			_, err := gw.Embed(ctx, providers.EmbeddingRequest{Model: "gpt-4o"})
			return err
		},
	},
	{
		name: "generate_image",
		call: func(ctx context.Context, gw *Gateway) error {
			_, err := gw.GenerateImage(ctx, providers.ImageRequest{Model: "gpt-4o"})
			return err
		},
	},
}

func newConcurrencyBoundGateway(t *testing.T, ep *blockingProvider, maxConcurrency, queueSize int) *Gateway {
	t.Helper()
	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets: []Target{{
			VirtualKey:  mockProviderName,
			Concurrency: &ConcurrencyConfig{MaxConcurrency: maxConcurrency, QueueSize: queueSize},
		}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(ep)
	return gw
}

func TestGateway_MultiModalSurfaces_SaturatedTargetShedsRequest(t *testing.T) {
	for _, tt := range multiModalCalls {
		t.Run(tt.name, func(t *testing.T) {
			ep := newBlockingProvider(mockProviderName)
			gw := newConcurrencyBoundGateway(t, ep, 1, 1)

			// Take the only in-flight slot.
			go func() { _ = tt.call(context.Background(), gw) }()
			waitFor(t, func() bool { return ep.inFlight.Load() == 1 })

			// Take the single queue seat.
			queuedErr := make(chan error, 1)
			go func() { queuedErr <- tt.call(context.Background(), gw) }()
			waitFor(t, func() bool { return gw.limiters[mockProviderName].waiting.Load() == 1 })

			// A third caller must be shed, not blocked.
			if err := tt.call(context.Background(), gw); !errors.Is(err, providers.ErrProviderSaturated) {
				t.Fatalf("%s on a saturated target = %v, want ErrProviderSaturated", tt.name, err)
			}

			close(ep.release)
			if err := <-queuedErr; err != nil {
				t.Errorf("queued %s = %v, want nil once a slot freed", tt.name, err)
			}
		})
	}
}

func TestGateway_MultiModalSurfaces_ReleaseSlotAfterCompletion(t *testing.T) {
	for _, tt := range multiModalCalls {
		t.Run(tt.name, func(t *testing.T) {
			ep := newBlockingProvider(mockProviderName)
			close(ep.release) // every call completes immediately
			gw := newConcurrencyBoundGateway(t, ep, 1, 1)

			if err := tt.call(context.Background(), gw); err != nil {
				t.Fatalf("%s: %v", tt.name, err)
			}
			// A second call must not be shed: the first call's slot was released.
			if err := tt.call(context.Background(), gw); err != nil {
				t.Errorf("%s after prior completion = %v, want nil: the slot was not released", tt.name, err)
			}
		})
	}
}

func TestGateway_MultiModalSurfaces_ReleaseSlotAfterCancellation(t *testing.T) {
	for _, tt := range multiModalCalls {
		t.Run(tt.name, func(t *testing.T) {
			ep := newBlockingProvider(mockProviderName)
			gw := newConcurrencyBoundGateway(t, ep, 1, 1)

			// Occupy the only slot.
			go func() { _ = tt.call(context.Background(), gw) }()
			waitFor(t, func() bool { return ep.inFlight.Load() == 1 })

			// A second caller waits for the slot, then is cancelled.
			ctx, cancel := context.WithCancel(context.Background())
			cancelledErr := make(chan error, 1)
			go func() { cancelledErr <- tt.call(ctx, gw) }()
			waitFor(t, func() bool { return gw.limiters[mockProviderName].waiting.Load() == 1 })
			cancel()
			if err := <-cancelledErr; !errors.Is(err, context.Canceled) {
				t.Fatalf("%s = %v, want context.Canceled", tt.name, err)
			}

			// The cancelled caller must not have left the slot occupied: once the
			// original holder releases, a fresh call must be admitted.
			close(ep.release)
			if err := tt.call(context.Background(), gw); err != nil {
				t.Errorf("%s after cancellation and release = %v, want nil", tt.name, err)
			}
		})
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
