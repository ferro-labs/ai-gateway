package aigateway

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/providers"
	"go.uber.org/goleak"
)

// stressStubProvider returns a fresh *providers.Response on every Complete
// call. The shared-pointer mockProvider in gateway_test.go is unsafe under
// concurrent Route() — gateway.go:519 writes OverheadMs on the returned
// pointer, so two parallel callers racing on the same pointer trips the race
// detector with a fixture-level race that has nothing to do with the bugs
// these tests are checking for.
type stressStubProvider struct {
	name   string
	models []string
}

func (s *stressStubProvider) Name() string                  { return s.name }
func (s *stressStubProvider) SupportedModels() []string     { return s.models }
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

// stressGoleak applies a goroutine-leak check at the end of each stress test.
// We can't use TestMain here because gateway_test.go runs many other tests in
// the same package that intentionally leak goroutines (e.g. hook workers from
// gateways that the test never Closes). Per-test goleak with IgnoreCurrent
// catches NEW leaks introduced by the test under inspection.
func stressGoleak(t *testing.T) {
	t.Helper()
	opts := []goleak.Option{
		goleak.IgnoreCurrent(),
		// stdlib HTTP/2 connection pool keeps a readLoop goroutine alive
		// until the transport's idle timeout. The catalog loader inside
		// gateway.New() opens one. It is not a gateway leak; ignore it.
		goleak.IgnoreTopFunction("net/http.(*http2ClientConn).readLoop"),
		goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),
	}
	t.Cleanup(func() {
		// Give any in-flight goroutines a brief grace window to exit. Without
		// this, a perfectly-correct goroutine that is mid-return when the
		// check runs can be flagged.
		deadline := time.Now().Add(2 * time.Second)
		var err error
		for time.Now().Before(deadline) {
			if err = goleak.Find(opts...); err == nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("goroutine leak detected: %v", err)
		}
	})
}

// TestStress_ShutdownUnderLoad_NoPanic is the C1 regression: pre-fix, calling
// Close() while publishEvent goroutines were in flight produced a hard panic
// (send on closed channel). The fix replaces close(hookDispatchQ) with a
// cancellation-context pattern. Run with -race to catch any reintroduction.
func TestStress_ShutdownUnderLoad_NoPanic(t *testing.T) {
	stressGoleak(t)

	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "stub"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(&stressStubProvider{name: "stub", models: []string{"gpt-4o"}})
	// At least one hook so hasHooks() returns true and publishEvent actually
	// enqueues into hookDispatchQ on every Route call.
	gw.AddHook(func(_ context.Context, _ string, _ map[string]interface{}) {})

	const workers = 50
	var wg sync.WaitGroup
	stop := make(chan struct{})

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
				// Ignore errors — the gateway may legitimately return errors
				// after Close(). The test only fails on PANIC.
				_, _ = gw.Route(context.Background(), req)
			}
		}()
	}

	// Let load build up, then shut down hard.
	time.Sleep(100 * time.Millisecond)
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
	stressGoleak(t)

	gw, err := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "stub"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = gw.Close() }()

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
		for {
			select {
			case <-stop:
				return
			default:
			}
			gw.mu.Lock()
			gw.circuitBreakers["stub"] = circuitbreaker.New(5, 1, 1, time.Second)
			gw.providers["stub"] = &stressStubProvider{name: "stub", models: []string{"gpt-4o"}}
			gw.mu.Unlock()
			mutations.Add(1)
			time.Sleep(time.Microsecond)
		}
	}()

	time.Sleep(300 * time.Millisecond)
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

	// Sanity-check the mutator actually got scheduled at all. -race catches
	// the data race at single-iteration granularity, so this is just a guard
	// against the mutator goroutine never starting (which would make the
	// whole test a no-op). A higher threshold here would be flaky on busy
	// CI runners under -cover, where the 300ms window can fit very few
	// iterations.
	if mutations.Load() < 1 {
		t.Fatalf("mutator never ran — test is not exercising the race")
	}
}
