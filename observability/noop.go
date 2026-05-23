package observability

import "context"

// NoOp returns a Provider that performs no work. Every method is a
// no-op suitable for inlining by the Go compiler — zero allocations
// on the hot path. This is the default when no exporters are
// configured.
//
// The invariant that wiring NoOp into the gateway via SetObservability
// adds zero allocations compared to the default gateway (which already
// uses NoOp internally) is asserted by TestRoute_TracingOff_AllocBaseline
// in gateway_observability_bench_test.go. BenchmarkRoute_TracingOff in
// the same file measures the absolute allocation trend over time.
func NoOp() Provider { return noopProvider{} }

type noopProvider struct{}

func (noopProvider) StartRequestSpan(ctx context.Context, _ RequestAttrs) (context.Context, Span) {
	return ctx, noopSpan{}
}

func (noopProvider) RecordEvent(_ context.Context, _ Event) {}

func (noopProvider) Shutdown(_ context.Context) error { return nil }

type noopSpan struct{}

func (noopSpan) StartChild(ctx context.Context, _ string, _ SpanKind) (context.Context, Span) {
	return ctx, noopSpan{}
}

func (noopSpan) SetAttribute(_ string, _ any)  {}
func (noopSpan) SetTokens(_, _, _ int)         {}
func (noopSpan) SetCost(_ CostBreakdown)       {}
func (noopSpan) SetError(_ error)              {}
func (noopSpan) SetStreamTimings(_, _ float64) {}
func (noopSpan) End()                          {}

// Compile-time interface guards.
var (
	_ Provider = (*noopProvider)(nil)
	_ Span     = (*noopSpan)(nil)
)
