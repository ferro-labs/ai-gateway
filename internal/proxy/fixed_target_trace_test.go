package proxy

import (
	"net/http/httptest"
	"testing"
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

func TestBatchHandler_TraceparentInjectionCanBeTurnedOff(t *testing.T) {
	upstream, seen := upstreamCapturingTraceHeaders(t)
	h := BatchHandler(noTraceBatchSource{fixedTargetSource(upstream.URL)})

	h(httptest.NewRecorder(), tracedRequest(t, "/v1/files"))

	if got := seen.Get("traceparent"); got != "" {
		t.Errorf("upstream traceparent = %q, want none with propagate_passthrough off", got)
	}
}
