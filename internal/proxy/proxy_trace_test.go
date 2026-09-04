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
)

// tracedRequest returns a pass-through request whose context carries a sampled
// span context, as it does after the gateway has opened the request span.
func tracedRequest(t *testing.T) *http.Request {
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
	req := httptest.NewRequestWithContext(trace.ContextWithSpanContext(t.Context(), sc), http.MethodPost, "/v1/files", nil)
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

	handler(httptest.NewRecorder(), tracedRequest(t))

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

	req := tracedRequest(t)
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

	handler(httptest.NewRecorder(), tracedRequest(t))

	if got := seen.Get("traceparent"); got != "" {
		t.Errorf("upstream traceparent = %q, want none with propagate_passthrough off", got)
	}
}

func TestProxyHandler_NoSpanContextInjectsNothing(t *testing.T) {
	upstream, seen := upstreamCapturingTraceHeaders(t)
	handler := Handler(buildTestRegistry(t, upstream.URL))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/files", nil)
	req.Header.Set("X-Provider", providerOpenAI)
	handler(httptest.NewRecorder(), req)

	if got := seen.Get("traceparent"); got != "" {
		t.Errorf("upstream traceparent = %q, want none when the request carries no trace", got)
	}
}
