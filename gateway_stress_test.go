package aigateway

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/providers"
)

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

// TestStress_ShutdownUnderLoad_NoPanic is the C1 regression: pre-fix, calling
// Close() while publishEvent goroutines were in flight produced a hard panic
// (send on closed channel). The fix replaces close(hookDispatchQ) with a
// cancellation-context pattern. Run with -race to catch any reintroduction.
func TestStress_ShutdownUnderLoad_NoPanic(t *testing.T) {
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "stub"}},
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
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "stub"}},
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
