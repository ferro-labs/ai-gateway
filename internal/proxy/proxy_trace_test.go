package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
	"go.opentelemetry.io/otel/trace"
)

const (
	testTraceID     = "0af7651916cd43dd8448eb211c80319c"
	testSpanID      = "b7ad6b7169203331"
	wantTraceparent = "00-" + testTraceID + "-" + testSpanID + "-01"

	// proxiedPath is a path only the generic /v1/* pass-through serves. Files,
	// batches and the responses id sub-routes have their own fixed-target
	// handler, and /v1/models is its own static route (router.go), so naming
	// any of those here would describe coverage these tests do not have.
	proxiedPath = "/v1/assistants"
)

// tracedRequest returns a request for path whose context carries a sampled span
// context, as it does after the gateway has opened the request span.
func tracedRequest(t *testing.T, path string) *http.Request {
	t.Helper()
	traceID, err := trace.TraceIDFromHex(testTraceID)
	if err != nil {
		t.Fatal(err)
	}
	spanID, err := trace.SpanIDFromHex(testSpanID)
	if err != nil {
		t.Fatal(err)
	}
	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled})
	req := httptest.NewRequestWithContext(trace.ContextWithSpanContext(t.Context(), sc), http.MethodPost, path, nil)
	req.Header.Set("X-Provider", providerOpenAI)
	return req
}

func upstreamCapturingTraceHeaders(t *testing.T) (*httptest.Server, *http.Header) {
	t.Helper()
	var seen http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)
	return upstream, &seen
}

func TestProxyHandler_InjectsTraceparentUpstream(t *testing.T) {
	upstream, seen := upstreamCapturingTraceHeaders(t)
	handler := Handler(buildTestRegistry(t, upstream.URL))

	handler(httptest.NewRecorder(), tracedRequest(t, proxiedPath))

	if got := seen.Get("traceparent"); got != wantTraceparent {
		t.Errorf("upstream traceparent = %q, want %q", got, wantTraceparent)
	}
}

// TestProxyHandler_StripsCallerIdentityHeaders proves the gateway-facing
// identity headers never reach the provider, even though ReverseProxy clones
// every inbound header into the outbound request before Rewrite runs. Without
// the explicit deletes in Rewrite, an inbound baggage/X-User-ID/X-Session-ID
// header would travel upstream verbatim — the identity leak trace-context
// injection is meant not to introduce.
func TestProxyHandler_StripsCallerIdentityHeaders(t *testing.T) {
	upstream, seen := upstreamCapturingTraceHeaders(t)
	handler := Handler(buildTestRegistry(t, upstream.URL))

	req := tracedRequest(t, proxiedPath)
	req.Header.Set("baggage", "user.id=alice,session.id=xyz")
	req.Header.Set("X-User-ID", "alice")
	req.Header.Set("X-Session-ID", "xyz")

	handler(httptest.NewRecorder(), req)

	if got := seen.Get("baggage"); got != "" {
		t.Errorf("upstream received baggage %q; caller identity headers must not be forwarded", got)
	}
	if got := seen.Get("X-User-ID"); got != "" {
		t.Errorf("upstream received X-User-ID %q; caller identity headers must not be forwarded", got)
	}
	if got := seen.Get("X-Session-ID"); got != "" {
		t.Errorf("upstream received X-Session-ID %q; caller identity headers must not be forwarded", got)
	}
	if got := seen.Get("traceparent"); got != wantTraceparent {
		t.Errorf("upstream traceparent = %q, want %q", got, wantTraceparent)
	}
}

// noTracePolicySource is a registry whose owner turned propagation off.
type noTracePolicySource struct{ *providers.Registry }

func (noTracePolicySource) PropagatesPassthroughTrace() bool { return false }

func TestProxyHandler_TraceparentInjectionCanBeTurnedOff(t *testing.T) {
	upstream, seen := upstreamCapturingTraceHeaders(t)
	handler := Handler(noTracePolicySource{buildTestRegistry(t, upstream.URL)})

	handler(httptest.NewRecorder(), tracedRequest(t, proxiedPath))

	if got := seen.Get("traceparent"); got != "" {
		t.Errorf("upstream traceparent = %q, want none with propagate_passthrough off", got)
	}
}

// TestProxyHandler_TurnedOffDropsInboundTraceHeaders proves propagation-off
// means no trace context reaches the provider, not merely that the gateway
// declines to add its own. ReverseProxy clones every inbound header into the
// outbound request before Rewrite runs, so a caller's own traceparent survives
// unless Rewrite deletes it — which would forward the caller's trace context
// upstream through a setting the operator turned off.
func TestProxyHandler_TurnedOffDropsInboundTraceHeaders(t *testing.T) {
	upstream, seen := upstreamCapturingTraceHeaders(t)
	handler := Handler(noTracePolicySource{buildTestRegistry(t, upstream.URL)})

	req := tracedRequest(t, proxiedPath)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	req.Header.Set("tracestate", "vendor=opaque")

	handler(httptest.NewRecorder(), req)

	if got := seen.Get("traceparent"); got != "" {
		t.Errorf("upstream traceparent = %q, want none: the caller's own header must not pass through with propagate_passthrough off", got)
	}
	if got := seen.Get("tracestate"); got != "" {
		t.Errorf("upstream tracestate = %q, want none: the caller's own header must not pass through with propagate_passthrough off", got)
	}
}

// TestProxyHandler_InboundTraceHeadersReplacedNotAppended proves the gateway's
// own trace context replaces the caller's rather than joining it, so the
// provider sees one traceparent — this gateway's — and never the caller's.
func TestProxyHandler_InboundTraceHeadersReplacedNotAppended(t *testing.T) {
	upstream, seen := upstreamCapturingTraceHeaders(t)
	handler := Handler(buildTestRegistry(t, upstream.URL))

	req := tracedRequest(t, proxiedPath)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	req.Header.Set("tracestate", "vendor=opaque")

	handler(httptest.NewRecorder(), req)

	if got := seen.Values("traceparent"); len(got) != 1 || got[0] != wantTraceparent {
		t.Errorf("upstream traceparent = %q, want exactly [%q]", got, wantTraceparent)
	}
	if got := seen.Get("tracestate"); got == "vendor=opaque" {
		t.Errorf("upstream tracestate = %q; the caller's tracestate must not be forwarded verbatim", got)
	}
}

func TestProxyHandler_NoSpanContextInjectsNothing(t *testing.T) {
	upstream, seen := upstreamCapturingTraceHeaders(t)
	handler := Handler(buildTestRegistry(t, upstream.URL))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, proxiedPath, nil)
	req.Header.Set("X-Provider", providerOpenAI)
	handler(httptest.NewRecorder(), req)

	if got := seen.Get("traceparent"); got != "" {
		t.Errorf("upstream traceparent = %q, want none when the request carries no trace", got)
	}
}
