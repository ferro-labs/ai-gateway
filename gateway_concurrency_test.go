package aigateway

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/config"
	internalmcp "github.com/ferro-labs/ai-gateway/mcp"
	"github.com/ferro-labs/ai-gateway/pkg/circuitbreaker"
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
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets: []config.Target{{
			VirtualKey:  mockProviderName,
			Concurrency: &config.ConcurrencyConfig{MaxConcurrency: maxConcurrency, QueueSize: queueSize},
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

// silentStreamProvider returns a stream channel and then never sends on it and
// never closes it — the shape of a provider that accepted the connection and
// hung. It ignores ctx on purpose: a well-behaved provider closes on
// cancellation, and the limiter must not depend on that to get its slot back.
type silentStreamProvider struct {
	mockProvider
	ch chan providers.StreamChunk
}

func (s *silentStreamProvider) CompleteStream(_ context.Context, _ providers.Request) (<-chan providers.StreamChunk, error) {
	return s.ch, nil
}

// TestLimitedProvider_StreamReleasesSlotWhenUpstreamNeverSends is the regression
// guard for a forwarder that selected on ctx.Done() only at the SEND. With
// nothing ever arriving to send, that select was never reached, so the goroutine
// sat blocked on the receive and pinned the target's only in-flight slot for as
// long as the provider stayed silent — which for a hung upstream is forever,
// stalling every queued request behind it.
func TestLimitedProvider_StreamReleasesSlotWhenUpstreamNeverSends(t *testing.T) {
	inner := &silentStreamProvider{
		mockProvider: mockProvider{name: "silent", models: []string{"gpt-4o"}},
		ch:           make(chan providers.StreamChunk),
	}
	lim := newProviderLimiter(1, 0) // exactly one in-flight slot
	sp, ok := decorateProvider("silent", inner, nil, lim).(providers.StreamProvider)
	if !ok {
		t.Fatal("decorated stream provider must satisfy StreamProvider")
	}

	ctx, cancel := context.WithCancel(context.Background())
	out, err := sp.CompleteStream(ctx, providers.Request{Model: "gpt-4o"})
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}

	// The stream is live and silent, so the slot is legitimately held.
	busyCtx, busyCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer busyCancel()
	if err := lim.acquire(busyCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("acquire during a live stream = %v, want the slot to still be held", err)
	}

	// The caller goes away. The slot must come back on the cancellation alone,
	// without the provider ever sending or closing.
	cancel()
	waitFor(t, func() bool {
		select {
		case lim.slots <- struct{}{}:
			<-lim.slots
			return true
		default:
			return false
		}
	})

	// The forwarder must also have closed its own channel, or a consumer still
	// ranging over it hangs just as long.
	select {
	case _, open := <-out:
		if open {
			t.Fatal("forwarder sent a chunk from a provider that never sent one")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("forwarder did not close its output channel after cancellation")
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

// freshMockProvider returns a new *providers.Response on every call, avoiding
// the shared-pointer data race that would occur if multiple goroutines mutate
// a single response returned by the default mockProvider.
type freshMockProvider struct {
	name   string
	models []string
}

func (p *freshMockProvider) Name() string                  { return p.name }
func (p *freshMockProvider) ConfiguredModels() []string    { return p.models }
func (p *freshMockProvider) Models() []providers.ModelInfo { return nil }
func (p *freshMockProvider) SupportsModel(model string) bool {
	for _, m := range p.models {
		if m == model {
			return true
		}
	}
	return false
}
func (p *freshMockProvider) Complete(_ context.Context, _ providers.Request) (*providers.Response, error) {
	return &providers.Response{
		ID:      "r1",
		Choices: []providers.Choice{{Message: providers.Message{Role: "assistant", Content: "ok"}}},
	}, nil
}

// TestRoute_MCPSnapshotIsolation verifies that Route uses the mcpRegistry /
// mcpExecutor values captured at request entry, not whatever the live fields
// contain later. Concretely: if the registry is nil when the snapshot is taken,
// no MCP tool injection occurs even if a registry is installed after the lock
// is released.
func TestRoute_MCPSnapshotIsolation(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "mock"}},
	})

	if err != nil {
		t.Fatal(err)
	}
	gw.RegisterProvider(&freshMockProvider{
		name:   "mock",
		models: []string{"gpt-4o"},
	})

	// mcpRegistry is nil at this point — snapshot taken inside Route will be nil.
	resp, err := gw.Route(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Route() error: %v", err)
	}
	if len(resp.Choices) == 0 {
		t.Fatal("expected at least one choice")
	}
	// No MCP tools should have been injected (registry was nil at snapshot time).
	if len(resp.Choices[0].Message.ToolCalls) != 0 {
		t.Errorf("unexpected tool calls: %v", resp.Choices[0].Message.ToolCalls)
	}
}

// TestRoute_MCPFieldsSnapshotRace verifies that concurrent writes to
// g.mcpRegistry / g.mcpExecutor (under g.mu.Lock) do not race with Route's
// reads of those fields.
//
// Before the fix, Route read g.mcpRegistry and g.mcpExecutor after releasing
// g.mu, so a concurrent writer could replace the pointers mid-flight.  The
// fix snapshots both fields inside the initial g.mu.RLock block.
//
// Run with: go test -race -run TestRoute_MCPFieldsSnapshotRace .
func TestRoute_MCPFieldsSnapshotRace(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "mock"}},
	})

	if err != nil {
		t.Fatal(err)
	}
	gw.RegisterProvider(&freshMockProvider{
		name:   "mock",
		models: []string{"gpt-4o"},
	})

	req := providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	}

	var wg sync.WaitGroup
	const iters = 300

	// Writer: toggles mcpRegistry/mcpExecutor under the gateway lock, exactly
	// as ReloadConfig does (but without touching any other field, so we isolate
	// only the MCP snapshot race).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			gw.mu.Lock()
			reg := internalmcp.NewRegistry(nil)
			gw.mcpRegistry = reg
			gw.mcpExecutor = internalmcp.NewExecutor(reg, 5, nil)
			gw.mu.Unlock()

			gw.mu.Lock()
			gw.mcpRegistry = nil
			gw.mcpExecutor = nil
			gw.mu.Unlock()
		}
	}()

	// Readers: call Route concurrently with the writer.
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				_, _ = gw.Route(context.Background(), req)
			}
		}()
	}

	wg.Wait()
}

// TestRouteStream_MCPRegistrySnapshotRace verifies that RouteStream's
// mcpRegistry snapshot (taken inside the initial g.mu.RLock block) does not
// race with concurrent writes to g.mcpRegistry.
//
// The old code acquired a *second* g.mu.RLock just for the hasMCP check; the
// fix collapses it into the first lock block so the field is read exactly once,
// under the same lock that protects all other request-entry snapshots.
func TestRouteStream_MCPRegistrySnapshotRace(t *testing.T) {
	ch := make(chan providers.StreamChunk)
	close(ch)

	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "mock-stream"}},
	})

	if err != nil {
		t.Fatal(err)
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{
			name:   "mock-stream",
			models: []string{"gpt-4o"},
		},
		streamCh: ch,
	})

	req := providers.Request{
		Model:    "gpt-4o",
		Stream:   true,
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	}

	var wg sync.WaitGroup
	const iters = 300

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			gw.mu.Lock()
			reg := internalmcp.NewRegistry(nil)
			gw.mcpRegistry = reg
			gw.mcpExecutor = internalmcp.NewExecutor(reg, 5, nil)
			gw.mu.Unlock()

			gw.mu.Lock()
			gw.mcpRegistry = nil
			gw.mcpExecutor = nil
			gw.mu.Unlock()
		}
	}()

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				out, err := gw.RouteStream(context.Background(), req)
				if err != nil {
					continue
				}
				for range out { //nolint:revive // empty-block: intentionally draining the stream to completion
				}
			}
		}()
	}

	wg.Wait()
}

// stressStubProvider returns a fresh *providers.Response on every Complete
// call. The shared-pointer mockProvider in gateway_helpers_test.go is unsafe under
// concurrent Route() — gateway.go:519 writes OverheadMs on the returned
// pointer, so two parallel callers racing on the same pointer trips the race
// detector with a fixture-level race that has nothing to do with the bugs
// these tests are checking for.
type stressStubProvider struct {
	name   string
	models []string
}

func (s *stressStubProvider) Name() string                  { return s.name }
func (s *stressStubProvider) ConfiguredModels() []string    { return s.models }
func (s *stressStubProvider) Models() []providers.ModelInfo { return nil }
func (s *stressStubProvider) SupportsModel(model string) bool {
	for _, m := range s.models {
		if m == model {
			return true
		}
	}
	return false
}
func (s *stressStubProvider) Complete(_ context.Context, _ providers.Request) (*providers.Response, error) {
	return &providers.Response{ID: "ok", Provider: s.name, Model: "gpt-4o"}, nil
}

// TestStress_ShutdownUnderLoad_NoPanic is the C1 regression: pre-fix, calling
// Close() while publishEvent goroutines were in flight produced a hard panic
// (send on closed channel). The fix replaces close(hookDispatchQ) with a
// cancellation-context pattern. Run with -race to catch any reintroduction.
func TestStress_ShutdownUnderLoad_NoPanic(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "stub"}},
	})

	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(&stressStubProvider{name: "stub", models: []string{"gpt-4o"}})
	// At least one hook so hasHooks() returns true and publishEvent actually
	// enqueues into hookDispatchQ on every Route call.
	gw.AddHook(func(_ context.Context, _ string, _ map[string]any) {})

	const workers = 50
	var wg sync.WaitGroup
	stop := make(chan struct{})
	started := make(chan struct{}, workers)

	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := providers.Request{
				Model:    "gpt-4o",
				Messages: []providers.Message{{Role: "user", Content: "hi"}},
			}
			_, _ = gw.Route(context.Background(), req)
			started <- struct{}{}
			for {
				select {
				case <-stop:
					return
				default:
				}
				// Ignore errors — the gateway may legitimately return errors
				// after Close(). The test only fails on PANIC.
				_, _ = gw.Route(context.Background(), req)
			}
		}()
	}

	for range workers {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("route worker did not start")
		}
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	close(stop)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("workers did not exit within 5s of Close()")
	}

	// Idempotency: a second Close must not panic either.
	if err := gw.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestStress_ReloadUnderLoad_NoRace is the C2 regression: pre-fix, the lookup
// closure inside getStrategy read g.providers and g.circuitBreakers without
// holding g.mu, racing ReloadConfig (which reassigns circuitBreakers
// wholesale at line ~707) and RegisterProvider (writes g.providers under
// Lock). Must be run with -race.
func TestStress_ReloadUnderLoad_NoRace(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "stub"}},
	})

	if err != nil {
		t.Fatalf("New: %v", err)
	}

	gw.RegisterProvider(&stressStubProvider{name: "stub", models: []string{"gpt-4o"}})

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Request workload: 20 goroutines hammering Route through the strategy
	// (which invokes the lookup closure on every call).
	const workers = 20
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := providers.Request{
				Model:    "gpt-4o",
				Messages: []providers.Message{{Role: "user", Content: "hi"}},
			}
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = gw.Route(context.Background(), req)
			}
		}()
	}

	// Mutator: races provider/circuit-breaker writes vs the lookup reads.
	// Uses the same write paths the gateway uses in production (Register +
	// direct map mutation via g.mu) so the race is real, not synthetic.
	wg.Add(1)
	var mutations atomic.Int64
	go func() {
		defer wg.Done()
		for range 100 {
			gw.mu.Lock()
			gw.circuitBreakers["stub"] = circuitbreaker.New(5, 1, 1, time.Second)
			gw.providers["stub"] = &stressStubProvider{name: "stub", models: []string{"gpt-4o"}}
			gw.mu.Unlock()
			mutations.Add(1)
		}
	}()

	for mutations.Load() < 100 {
		runtime.Gosched()
	}
	close(stop)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("workers did not exit within 5s")
	}
}
