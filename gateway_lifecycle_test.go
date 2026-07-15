package aigateway

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/events"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/providers"
)

func TestGateway_CloseIsIdempotent(t *testing.T) {
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "unused"}},
	})

	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// gateMockProvider blocks in Complete until release is closed, so tests can
// overlap provider execution with Gateway.Close().
type gateMockProvider struct {
	mockProvider
	release     chan struct{}
	entered     chan struct{}
	active      atomic.Int32
	releaseOnce sync.Once
}

func newGateMockProvider(t *testing.T, resp *providers.Response, err error) *gateMockProvider {
	t.Helper()
	p := &gateMockProvider{
		mockProvider: mockProvider{
			name:   "gate",
			models: []string{"gpt-4o"},
			resp:   resp,
			err:    err,
		},
		release: make(chan struct{}),
		entered: make(chan struct{}, 64),
	}
	// Release blocked Complete goroutines even if the test fails before its
	// explicit releaseAll, so a failed assertion can never leak them.
	t.Cleanup(p.releaseAll)
	return p
}

func (p *gateMockProvider) Complete(_ context.Context, _ providers.Request) (*providers.Response, error) {
	p.active.Add(1)
	p.entered <- struct{}{}
	<-p.release
	if p.err != nil {
		return nil, p.err
	}
	if p.resp == nil {
		return nil, nil
	}
	resp := *p.resp
	return &resp, nil
}

func (p *gateMockProvider) releaseAll() {
	p.releaseOnce.Do(func() { close(p.release) })
}

func (p *gateMockProvider) waitActive(t *testing.T, want int32) {
	t.Helper()
	for range want {
		select {
		case <-p.entered:
		case <-time.After(2 * time.Second):
			t.Fatalf("active routes = %d, want %d", p.active.Load(), want)
		}
	}
}

// gateStreamProvider blocks before emitting stream chunks until release closes.
type gateStreamProvider struct {
	mockStreamProvider
	enterOnce   sync.Once
	enter       chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
}

func newGateStreamProvider(t *testing.T) *gateStreamProvider {
	t.Helper()
	p := &gateStreamProvider{
		mockStreamProvider: mockStreamProvider{
			mockProvider: mockProvider{
				name:   "gate-stream",
				models: []string{"gpt-4o"},
			},
		},
		enter:   make(chan struct{}),
		release: make(chan struct{}),
	}
	t.Cleanup(p.releaseAll)
	return p
}

// releaseAll unblocks the stream goroutine. It is idempotent so a test can call
// it explicitly and still register it with t.Cleanup as a leak-safety net.
func (p *gateStreamProvider) releaseAll() {
	p.releaseOnce.Do(func() { close(p.release) })
}

func (p *gateStreamProvider) CompleteStream(_ context.Context, _ providers.Request) (<-chan providers.StreamChunk, error) {
	ch := make(chan providers.StreamChunk, 1)
	go func() {
		p.enterOnce.Do(func() { close(p.enter) })
		<-p.release
		ch <- providers.StreamChunk{
			ID:     "stream-1",
			Object: "chat.completion.chunk",
			Model:  "gpt-4o",
			Choices: []providers.StreamChoice{{
				Index: 0,
				Delta: providers.MessageDelta{Role: "assistant", Content: "hi"},
			}},
		}
		close(ch)
	}()
	return ch, nil
}

func completedHookEvent(traceID string) events.HookEvent {
	return events.CompletedRequest(
		traceID,
		mockProviderName,
		"gpt-4o",
		time.Millisecond,
		false,
		1,
		1,
		models.CostResult{},
		true,
	)
}

// TestGateway_PublishEvent_DetachesCancellationButPreservesValues covers
// issue #181: async event hooks must run with a context that has shed the
// request's cancellation (they fire after the HTTP handler returns) yet still
// carry the request's trace context / values via context.WithoutCancel.
func TestGateway_PublishEvent_DetachesCancellationButPreservesValues(t *testing.T) {
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "unused"}},
	})

	if err != nil {
		t.Fatalf("New: %v", err)
	}

	type ctxKey string
	const marker ctxKey = "trace-marker"

	got := make(chan context.Context, 1)
	gw.AddHook(func(ctx context.Context, _ string, _ map[string]any) {
		got <- ctx
	})

	// The request context is already cancelled by the time the hook runs.
	reqCtx, cancel := context.WithCancel(context.WithValue(context.Background(), marker, "trace-xyz"))
	cancel()

	gw.publishEvent(reqCtx, completedHookEvent("trace-1"))

	select {
	case hookCtx := <-got:
		if err := hookCtx.Err(); err != nil {
			t.Fatalf("hook ctx should be detached from cancellation, got %v", err)
		}
		if v, _ := hookCtx.Value(marker).(string); v != "trace-xyz" {
			t.Fatalf("hook ctx lost request trace value: got %q, want trace-xyz", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hook was not dispatched")
	}
}

func runWithPanicCapture(t *testing.T, fn func()) any {
	t.Helper()
	done := make(chan any, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- r
				return
			}
			done <- nil
		}()
		fn()
	}()

	select {
	case r := <-done:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for goroutine")
		return nil
	}
}

func newHookedGateway(t *testing.T, provider providers.Provider) (*Gateway, *gateMockProvider) {
	t.Helper()
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: provider.Name()}},
	})

	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gate, ok := provider.(*gateMockProvider)
	if !ok {
		t.Fatalf("provider must be *gateMockProvider, got %T", provider)
	}
	gw.RegisterProvider(gate)
	gw.AddHook(func(context.Context, string, map[string]any) {})
	return gw, gate
}

func TestGateway_Close_DuringInFlightRouteDoesNotPanic(t *testing.T) {
	provider := newGateMockProvider(t, &providers.Response{ID: "ok", Model: "gpt-4o"}, nil)
	gw, gate := newHookedGateway(t, provider)

	routeDone := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				routeDone <- fmt.Errorf("panic: %v", r)
			}
		}()
		_, err := gw.Route(context.Background(), providers.Request{
			Model:    "gpt-4o",
			Messages: []providers.Message{{Role: "user", Content: "hi"}},
		})
		routeDone <- err
	}()

	gate.waitActive(t, 1)
	if err := gw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	gate.releaseAll()

	select {
	case err := <-routeDone:
		if err != nil {
			t.Fatalf("Route failed during shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for in-flight route")
	}
}

func TestGateway_Close_DuringInFlightRouteStreamDoesNotPanic(t *testing.T) {
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "gate-stream"}},
	})

	if err != nil {
		t.Fatalf("New: %v", err)
	}

	streamProvider := newGateStreamProvider(t)
	gw.RegisterProvider(streamProvider)
	gw.AddHook(func(context.Context, string, map[string]any) {})

	routeDone := make(chan any, 1)
	go func() {
		var result any
		defer func() {
			if r := recover(); r != nil {
				result = r
			}
			routeDone <- result
		}()
		ch, err := gw.RouteStream(context.Background(), providers.Request{
			Model:    "gpt-4o",
			Messages: []providers.Message{{Role: "user", Content: "hi"}},
			Stream:   true,
		})
		if err != nil {
			result = err
			return
		}
		// Drain the stream.
		//nolint:revive // intentionally draining the stream channel to completion
		for range ch {
		}
	}()

	select {
	case <-streamProvider.enter:
	case <-time.After(time.Second):
		t.Fatal("stream provider never started")
	}

	if err := gw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	streamProvider.releaseAll()

	select {
	case result := <-routeDone:
		if result != nil {
			t.Fatalf("RouteStream failed or panicked: %v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for stream route to finish")
	}
}

func TestGateway_Close_DuringFailedRouteDoesNotPanic(t *testing.T) {
	provider := newGateMockProvider(t, nil, fmt.Errorf("provider down"))
	gw, gate := newHookedGateway(t, provider)

	routeDone := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				routeDone <- fmt.Errorf("panic: %v", r)
			}
		}()
		_, err := gw.Route(context.Background(), providers.Request{
			Model:    "gpt-4o",
			Messages: []providers.Message{{Role: "user", Content: "hi"}},
		})
		routeDone <- err
	}()

	gate.waitActive(t, 1)
	if err := gw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	gate.releaseAll()

	select {
	case err := <-routeDone:
		if err == nil {
			t.Fatal("expected route error, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for failed route")
	}
}

func TestGateway_PublishEvent_AfterCloseDoesNotPanic(t *testing.T) {
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "unused"}},
	})

	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.AddHook(func(context.Context, string, map[string]any) {})

	if err := gw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if p := runWithPanicCapture(t, func() {
		gw.publishEvent(context.Background(), completedHookEvent("trace-after-close"))
	}); p != nil {
		t.Fatalf("publishEvent panicked after Close: %v", p)
	}
}

func TestGateway_PublishEvent_AfterShutdownWithFullQueueDoesNotPanic(t *testing.T) {
	gw := &Gateway{
		hooks: newHookBus(1),
	}
	ctx, cancel := context.WithCancel(context.Background())
	gw.shutdownCtx = ctx
	gw.shutdownCancel = cancel
	workerCount := runtime.GOMAXPROCS(0)
	if workerCount < 1 {
		workerCount = 1
	}
	if workerCount > maxHookWorkers {
		workerCount = maxHookWorkers
	}
	entered := make(chan struct{}, workerCount)
	release := make(chan struct{})
	gw.AddHook(func(context.Context, string, map[string]any) {
		entered <- struct{}{}
		<-release
	})
	gw.hooks.start(gw.shutdownCtx)
	t.Cleanup(func() {
		cancel()
		close(release)
		gw.hooks.wait()
	})

	for i := range workerCount {
		gw.publishEvent(context.Background(), completedHookEvent(fmt.Sprintf("trace-block-worker-%d", i)))
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatalf("hook worker %d did not enter callback", i)
		}
	}

	gw.publishEvent(context.Background(), completedHookEvent("trace-fill-queue"))
	if got := len(gw.hooks.dispatchQ); got != 1 {
		t.Fatalf("dispatch queue length = %d, want 1", got)
	}

	gw.shutdownCancel()

	if p := runWithPanicCapture(t, func() {
		gw.publishEvent(context.Background(), completedHookEvent("trace-shutdown-full"))
	}); p != nil {
		t.Fatalf("publishEvent panicked after shutdown with full queue: %v", p)
	}
}

func TestGateway_Close_ConcurrentPublishEventStress(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping concurrent hook shutdown stress in -short")
	}

	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "unused"}},
	})

	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.AddHook(func(context.Context, string, map[string]any) {})

	const publishers = 32
	panicCh := make(chan any, publishers)
	started := make(chan struct{}, publishers)
	start := make(chan struct{})
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range publishers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					panicCh <- r
				}
			}()
			<-start
			gw.publishEvent(context.Background(), completedHookEvent("trace-stress"))
			started <- struct{}{}
			// Keep publishing until stop closes (which happens only after Close
			// returns) so the publishes deterministically overlap shutdown rather
			// than merely preceding it.
			for {
				select {
				case <-stop:
					return
				default:
					gw.publishEvent(context.Background(), completedHookEvent("trace-stress"))
				}
			}
		}()
	}

	close(start)
	for range publishers {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("publisher did not start")
		}
	}
	// Close while every publisher is still actively publishing.
	if err := gw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	close(stop)

	wg.Wait()
	close(panicCh)
	for p := range panicCh {
		t.Fatalf("concurrent publishEvent panicked during Close: %v", p)
	}
}

func TestGateway_Close_DuringConcurrentRoutesDoesNotPanic(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping concurrent route shutdown stress in -short")
	}

	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "gate"}},
	})

	if err != nil {
		t.Fatalf("New: %v", err)
	}

	provider := newGateMockProvider(t, &providers.Response{ID: "ok", Model: "gpt-4o"}, nil)
	gw.RegisterProvider(provider)
	gw.AddHook(func(context.Context, string, map[string]any) {})

	const routes = 16
	panicCh := make(chan any, routes)
	var wg sync.WaitGroup

	for range routes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					panicCh <- r
				}
			}()
			_, err := gw.Route(context.Background(), providers.Request{
				Model:    "gpt-4o",
				Messages: []providers.Message{{Role: "user", Content: "hi"}},
			})
			if err != nil {
				panicCh <- err
			}
		}()
	}

	provider.waitActive(t, int32(routes))

	if err := gw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	provider.releaseAll()

	wg.Wait()
	close(panicCh)
	for p := range panicCh {
		t.Fatalf("concurrent Route panicked during Close: %v", p)
	}
}

func TestGateway_Close_MultipleHooksDuringRouteDoesNotPanic(t *testing.T) {
	provider := newGateMockProvider(t, &providers.Response{ID: "ok", Model: "gpt-4o"}, nil)
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "gate"}},
	})

	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(provider)
	for range 3 {
		gw.AddHook(func(context.Context, string, map[string]any) {})
	}

	routeDone := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				routeDone <- fmt.Errorf("panic: %v", r)
			}
		}()
		_, err := gw.Route(context.Background(), providers.Request{
			Model:    "gpt-4o",
			Messages: []providers.Message{{Role: "user", Content: "hi"}},
		})
		routeDone <- err
	}()

	provider.waitActive(t, 1)
	if err := gw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	provider.releaseAll()

	select {
	case err := <-routeDone:
		if err != nil {
			t.Fatalf("Route failed or panicked with multiple hooks: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for route with multiple hooks")
	}
}
