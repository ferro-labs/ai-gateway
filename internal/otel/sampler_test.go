package otel

import (
	"context"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// sampleWithParent runs the sampler cfg describes against one span, optionally
// under a remote parent carrying the given sampled flag.
func sampleWithParent(t *testing.T, ratio float64, hasParent, parentSampled bool) sdktrace.SamplingDecision {
	t.Helper()

	tid := trace.TraceID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10}

	ctx := context.Background()
	if hasParent {
		var flags trace.TraceFlags
		if parentSampled {
			flags = trace.FlagsSampled
		}
		ctx = trace.ContextWithSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{
			TraceID:    tid,
			SpanID:     trace.SpanID{0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18},
			TraceFlags: flags,
			// Remote: the decision arrived over the wire in a traceparent
			// header, which is the case the gateway sits in.
			Remote: true,
		}))
	}

	return sampler(Config{SampleRatio: ratio}).ShouldSample(sdktrace.SamplingParameters{
		ParentContext: ctx,
		TraceID:       tid,
		Name:          "gateway.request",
		Kind:          trace.SpanKindServer,
	}).Decision
}

// TestInboundSampledTraceIsHonouredAtRatioZero is the regression test for
// inbound sampled traces being dropped.
//
// The sampler used to be a bare ratio sampler, which the specification requires
// to ignore the parent's sampled flag. A caller that sampled a trace and called
// this gateway got a span with no children: the trace was cut at the gateway.
func TestInboundSampledTraceIsHonouredAtRatioZero(t *testing.T) {
	if got := sampleWithParent(t, 0.0, true, true); got != sdktrace.RecordAndSample {
		t.Errorf("decision = %v, want RecordAndSample: an upstream that sampled this trace must not get a hole in it", got)
	}
}

// TestInboundUnsampledTraceIsHonouredAtRatioOne is the other direction, and it
// is the behaviour change operators will notice: an upstream that decided
// against sampling is respected here too, rather than overridden.
func TestInboundUnsampledTraceIsHonouredAtRatioOne(t *testing.T) {
	if got := sampleWithParent(t, 1.0, true, false); got != sdktrace.Drop {
		t.Errorf("decision = %v, want Drop: the upstream's decision not to sample is the trace's decision", got)
	}
}

// TestRootSpansStillFollowTheConfiguredRatio pins what sample_ratio governs
// after the change: traces this gateway starts.
func TestRootSpansStillFollowTheConfiguredRatio(t *testing.T) {
	if got := sampleWithParent(t, 1.0, false, false); got != sdktrace.RecordAndSample {
		t.Errorf("ratio 1.0 root decision = %v, want RecordAndSample", got)
	}
	if got := sampleWithParent(t, 0.0, false, false); got != sdktrace.Drop {
		t.Errorf("ratio 0.0 root decision = %v, want Drop", got)
	}
}

// TestSamplerIsParentBasedAtEveryRatio guards the wrapper itself, so a future
// edit cannot quietly return a bare sampler for one of the three branches.
// ParentBased's description names its delegate, which is how the three
// specification-standard configurations are told apart.
func TestSamplerIsParentBasedAtEveryRatio(t *testing.T) {
	for _, ratio := range []float64{0.0, 0.5, 1.0} {
		got := sampler(Config{SampleRatio: ratio}).Description()
		if len(got) < len("ParentBased") || got[:len("ParentBased")] != "ParentBased" {
			t.Errorf("sampler at ratio %v is %q, want a ParentBased sampler", ratio, got)
		}
	}
}
