package aigateway

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/events"
	"github.com/ferro-labs/ai-gateway/models"
)

func BenchmarkGateway_PublishEvent(b *testing.B) {
	silenceLogs(b)
	gw, err := newTestGateway(b, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "unused"}},
	})

	if err != nil {
		b.Fatal(err)
	}

	var calls atomic.Int64
	gw.AddHook(func(context.Context, string, map[string]any) {
		calls.Add(1)
	})

	event := events.CompletedRequest(
		"trace-bench",
		"bench",
		"gpt-4o",
		time.Millisecond,
		false,
		1,
		1,
		models.CostResult{},
		true,
	)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		gw.publishEvent(ctx, event)
	}
	b.StopTimer()

	if err := gw.Close(); err != nil {
		b.Fatalf("Close: %v", err)
	}
	b.ReportMetric(float64(calls.Load())/float64(b.N), "hooks/op")
}

// freshProvider returns a new *providers.Response on every Complete call so
// concurrent goroutines never share a response pointer. Used by race tests.
