package aigateway

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/providers"
)

// snapProvider is a provider that admits exactly its own models, so a lookup
// resolving to it proves the index and the provider map agreed.
type snapProvider struct {
	name   string
	models []string
}

func (s *snapProvider) Name() string               { return s.name }
func (s *snapProvider) ConfiguredModels() []string { return s.models }
func (s *snapProvider) SupportsModel(model string) bool {
	for _, m := range s.models {
		if m == model {
			return true
		}
	}
	return false
}

func (s *snapProvider) Complete(context.Context, providers.Request) (*providers.Response, error) {
	return nil, nil
}

func newSnapshotGateway(tb testing.TB) *Gateway {
	tb.Helper()
	gw, err := newTestGateway(tb, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "snap-0"}},
	})
	if err != nil {
		tb.Fatalf("new gateway: %v", err)
	}
	return gw
}

// A published snapshot must never be edited by a later rebuild. If it were, a
// reader holding one would observe the provider set changing underneath it —
// the exact hazard the type exists to remove.
func TestRoutingSnapshotIsFrozenAfterPublish(t *testing.T) {
	gw := newSnapshotGateway(t)
	gw.RegisterProvider(&snapProvider{name: "snap-0", models: []string{"model-a"}})

	before := gw.routing()
	if _, ok := before.findProvider("model-a"); !ok {
		t.Fatal("model-a does not resolve in the first snapshot")
	}

	gw.RegisterProvider(&snapProvider{name: "snap-1", models: []string{"model-b"}})

	// The snapshot taken before the second registration must still describe the
	// world as it was.
	//
	// Asserted on the map directly, because the indirect checks do not catch
	// the failure this guards. A snapshot aliasing g.providers still reports
	// the old providerNames — that field is a slice header copied by value, so
	// an append is invisible through it — and findProvider still misses the new
	// model, because the scan it falls back to walks that same stale
	// providerNames. The shared map is the whole hazard and the only thing that
	// shows it.
	if _, leaked := before.providers["snap-1"]; leaked {
		t.Fatal("published snapshot aliases the live provider map: a later registration mutated it")
	}
	if len(before.providers) != 1 {
		t.Fatalf("published snapshot mutated: providers = %v, want 1 entry", before.providers)
	}
	if len(before.providerNames) != 1 {
		t.Fatalf("published snapshot mutated: providerNames = %v, want 1 entry", before.providerNames)
	}
	if _, ok := before.findProvider("model-b"); ok {
		t.Fatal("published snapshot gained a provider registered after it was published")
	}

	after := gw.routing()
	if _, ok := after.findProvider("model-b"); !ok {
		t.Fatal("model-b does not resolve in the snapshot published after registering it")
	}
	if before == after {
		t.Fatal("registering a provider did not publish a new snapshot")
	}
}

// The index and the provider map must come from one generation. A lookup that
// consulted a new index against an old map would return "found" with a nil
// provider, or miss a provider the index names. Rebuilding while lookups run
// is what makes that observable.
func TestRoutingSnapshotStaysConsistentUnderConcurrentRebuild(t *testing.T) {
	gw := newSnapshotGateway(t)
	for i := range 8 {
		gw.RegisterProvider(&snapProvider{
			name:   fmt.Sprintf("snap-%d", i),
			models: []string{fmt.Sprintf("model-%d", i)},
		})
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Rebuilds, continuously.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			gw.RegisterProvider(&snapProvider{
				name:   fmt.Sprintf("churn-%d", i%4),
				models: []string{fmt.Sprintf("churn-model-%d", i%4)},
			})
		}
	}()

	// Readers, resolving models that were registered before the churn began and
	// must therefore resolve on every generation.
	for r := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 2000 {
				model := fmt.Sprintf("model-%d", r)
				p, ok := gw.routing().findProvider(model)
				if !ok {
					t.Errorf("%s stopped resolving during a rebuild", model)
					return
				}
				if p == nil {
					t.Errorf("%s resolved to a nil provider: index and provider map disagreed", model)
					return
				}
			}
		}()
	}

	close(stop)
	wg.Wait()
}
