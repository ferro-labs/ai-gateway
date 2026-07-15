package aigateway

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

// freshProvider returns a new *providers.Response on every Complete call so
// concurrent goroutines never share a response pointer. Used by race tests.
type freshProvider struct {
	name   string
	models []string
}

func (f *freshProvider) Name() string                  { return f.name }
func (f *freshProvider) SupportedModels() []string     { return f.models }
func (f *freshProvider) Models() []providers.ModelInfo { return nil }
func (f *freshProvider) SupportsModel(model string) bool {
	for _, m := range f.models {
		if m == model {
			return true
		}
	}
	return false
}
func (f *freshProvider) Complete(_ context.Context, req providers.Request) (*providers.Response, error) {
	return &providers.Response{ID: "r", Model: req.Model, Provider: f.name}, nil
}

// TestGateway_RouteProviderLookupNoDataRace is the acceptance test for issue #128.
//
// The lookup closure built inside getStrategy runs inside Strategy.Execute with
// no lock held. If it reads g.providers / g.circuitBreakers directly instead of
// from a snapshot taken under lock, a concurrent RegisterProvider (or
// runDiscovery) that writes those maps will cause a fatal data race.
//
// Run with -race to verify: go test -race -run TestGateway_RouteProviderLookupNoDataRace
func TestGateway_RouteProviderLookupNoDataRace(t *testing.T) {
	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeFallback},
		Targets: []Target{
			{VirtualKey: "p1"},
			{VirtualKey: "p2"},
		},
	})

	if err != nil {
		t.Fatal(err)
	}
	gw.RegisterProvider(&freshProvider{name: "p1", models: []string{"test-model"}})
	gw.RegisterProvider(&freshProvider{name: "p2", models: []string{"test-model"}})

	const routerGoroutines = 20
	const writerGoroutines = 10
	const iters = 40

	ctx := context.Background()
	req := providers.Request{
		Model:    "test-model",
		Messages: []providers.Message{{Role: roleUser, Content: "hello"}},
	}

	var wg sync.WaitGroup

	// Goroutines calling Route concurrently — these will execute the lookup
	// closure while the writers below mutate g.providers under lock.
	for i := 0; i < routerGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				_, _ = gw.Route(ctx, req)
			}
		}()
	}

	// Goroutines calling RegisterProvider concurrently — mirrors runtime
	// model discovery writing g.providers under lock (issue #128 trigger).
	for i := 0; i < writerGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iters/2; j++ {
				gw.RegisterProvider(&freshProvider{
					name:   fmt.Sprintf("dynamic-%d-%d", id, j),
					models: []string{"other-model"},
				})
			}
		}(i)
	}

	wg.Wait()
}
