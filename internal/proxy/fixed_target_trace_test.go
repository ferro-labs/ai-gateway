package proxy

import (
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel/propagation"
)

// fixedTargetSource points the Files/Batches surface at one upstream.
func fixedTargetSource(upstreamURL string) *batchSourceStub {
	return &batchSourceStub{target: providerOpenAI, provider: &batchStub{name: providerOpenAI, baseURL: upstreamURL}}
}

// noTraceBatchSource is a fixed-target source whose owner turned propagation off.
type noTraceBatchSource struct{ *batchSourceStub }

func (noTraceBatchSource) PropagatesPassthroughTrace() bool { return false }

// TestBatchHandler_StripsCallerIdentityHeaders proves the fixed-target surfaces
// (files, batches, the responses id sub-routes) hold the same line the generic
// pass-through does: ReverseProxy clones every inbound header into the outbound
// request, so a caller's baggage / X-User-ID / X-Session-ID would otherwise
// reach the provider verbatim.
func TestBatchHandler_StripsCallerIdentityHeaders(t *testing.T) {
	upstream, seen := upstreamCapturingTraceHeaders(t)
	h := BatchHandler(fixedTargetSource(upstream.URL))

	req := tracedRequest(t, "/v1/files")
	req.Header.Set("baggage", "user.id=alice,session.id=xyz")
	req.Header.Set("X-User-ID", "alice")
	req.Header.Set("X-Session-ID", "xyz")

	h(httptest.NewRecorder(), req)

	for _, name := range gatewayIdentityHeaders {
		if got := seen.Get(name); got != "" {
			t.Errorf("upstream received %s %q; caller identity headers must not reach the provider", name, got)
		}
	}
}

func TestBatchHandler_InjectsTraceparentUpstream(t *testing.T) {
	upstream, seen := upstreamCapturingTraceHeaders(t)
	h := BatchHandler(fixedTargetSource(upstream.URL))

	h(httptest.NewRecorder(), tracedRequest(t, "/v1/files"))

	if got := seen.Get("traceparent"); got != wantTraceparent {
		t.Errorf("upstream traceparent = %q, want %q", got, wantTraceparent)
	}
}

// TestBatchHandler_DoesNotEchoCallersTraceContext proves the fixed-target
// surfaces never re-inject a caller's own trace context. BatchHandler opens no
// gateway span, so the only span context reachable from the request is the
// one otel.Middleware extracted from the caller's inbound headers — and that
// extracted context is remote, not one the gateway owns. Injecting it back
// would make the shipped contract false: the caller's traceparent would ride
// upstream as if it were this gateway's.
func TestBatchHandler_DoesNotEchoCallersTraceContext(t *testing.T) {
	upstream, seen := upstreamCapturingTraceHeaders(t)
	h := BatchHandler(fixedTargetSource(upstream.URL))

	req := httptest.NewRequestWithContext(t.Context(), "POST", "/v1/files", nil)
	req.Header.Set("X-Provider", providerOpenAI)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	req.Header.Set("tracestate", "vendor=opaque")

	// Mirror what internal/otel.Middleware does on every inbound request:
	// extract the caller's W3C trace context into the request context.
	propagator := propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})
	ctx := propagator.Extract(req.Context(), propagation.HeaderCarrier(req.Header))
	req = req.WithContext(ctx)

	h(httptest.NewRecorder(), req)

	if got := seen.Get("traceparent"); got != "" {
		t.Errorf("upstream traceparent = %q, want none: the caller's own trace context must not be echoed back on a route with no gateway span", got)
	}
	if got := seen.Get("tracestate"); got != "" {
		t.Errorf("upstream tracestate = %q, want none: the caller's own trace context must not be echoed back on a route with no gateway span", got)
	}
}

func TestBatchHandler_TraceparentInjectionCanBeTurnedOff(t *testing.T) {
	upstream, seen := upstreamCapturingTraceHeaders(t)
	h := BatchHandler(noTraceBatchSource{fixedTargetSource(upstream.URL)})

	h(httptest.NewRecorder(), tracedRequest(t, "/v1/files"))

	if got := seen.Get("traceparent"); got != "" {
		t.Errorf("upstream traceparent = %q, want none with propagate_passthrough off", got)
	}
}
