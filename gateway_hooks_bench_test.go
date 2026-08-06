package aigateway

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/internal/events"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
)

// silenceLogs redirects the gateway's package logger to io.Discard for the
// duration of a benchmark, keeping JSON log lines out of the benchmark output
// (the gateway logger writes to os.Stdout by design).
func silenceLogs(b *testing.B) {
	b.Helper()
	prev := logger.Default()
	logger.SetDefault(logger.New(logger.Options{Output: io.Discard}))
	b.Cleanup(func() { logger.SetDefault(prev) })
}

func BenchmarkGateway_PublishEvent(b *testing.B) {
	silenceLogs(b)
	gw, err := newTestGateway(b, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "unused"}},
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
