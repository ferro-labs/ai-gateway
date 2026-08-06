package aigateway

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
	"github.com/ferro-labs/ai-gateway/providers"
)

// discoveryStub is a provider whose DiscoverModels takes a fixed amount of
// wall-clock time, standing in for a provider /models round trip.
type discoveryStub struct {
	name    string
	latency time.Duration
	models  []providers.ModelInfo
}

func (d *discoveryStub) Name() string               { return d.name }
func (d *discoveryStub) SupportsModel(string) bool  { return true }
func (d *discoveryStub) ConfiguredModels() []string { return nil }
func (d *discoveryStub) Complete(context.Context, providers.Request) (*providers.Response, error) {
	return nil, nil
}

func (d *discoveryStub) DiscoverModels(ctx context.Context) ([]providers.ModelInfo, error) {
	select {
	case <-time.After(d.latency):
		return d.models, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// benchDiscoveryProviders is the number of in-tree providers implementing
// DiscoveryProvider, so the benchmark sizes a real refresh rather than a
// convenient one.
const benchDiscoveryProviders = 19

func benchDiscoveryGateway(tb testing.TB, n int, latency time.Duration) *Gateway {
	tb.Helper()
	gw, err := newTestGateway(tb, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "stub-0"}},
	})
	if err != nil {
		tb.Fatalf("new gateway: %v", err)
	}
	for i := range n {
		name := fmt.Sprintf("stub-%d", i)
		gw.RegisterProvider(&discoveryStub{
			name:    name,
			latency: latency,
			models: []providers.ModelInfo{
				{ID: name + "-model-a", Object: "model", OwnedBy: name},
				{ID: name + "-model-b", Object: "model", OwnedBy: name},
			},
		})
	}
	return gw
}

// BenchmarkRunDiscovery covers the network shape: a refresh must cost about
// ceil(n/discoveryConcurrency) round trips, not n of them. Re-serializing the
// walk shows up here as roughly a 6x regression.
func BenchmarkRunDiscovery(b *testing.B) {
	gw := benchDiscoveryGateway(b, benchDiscoveryProviders, 20*time.Millisecond)
	log := logger.Default()
	for b.Loop() {
		gw.runDiscovery(context.Background(), log)
	}
}

// BenchmarkRunDiscoveryNoLatency isolates the CPU half. A refresh applies its
// results in one write and rebuilds the model index once; rebuilding per
// provider re-derives every other provider's catalog cache on each pass, which
// measured 16x this figure before the fan-out landed.
func BenchmarkRunDiscoveryNoLatency(b *testing.B) {
	gw := benchDiscoveryGateway(b, benchDiscoveryProviders, 0)
	log := logger.Default()
	for b.Loop() {
		gw.runDiscovery(context.Background(), log)
	}
}
