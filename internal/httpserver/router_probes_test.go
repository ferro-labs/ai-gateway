package httpserver_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/internal/admin/repository"
	"github.com/ferro-labs/ai-gateway/internal/httpserver"
	"github.com/ferro-labs/ai-gateway/pkg/ratelimit"
	"github.com/ferro-labs/ai-gateway/providers"
)

// buildTestRouterWithRateLimit is like buildTestRouter but wires a real
// per-client rate limiter so a test can drive it to exhaustion.
func buildTestRouterWithRateLimit(t *testing.T, gw *aigateway.Gateway, rlStore *ratelimit.Store) http.Handler {
	t.Helper()
	t.Setenv("ALLOW_UNAUTHENTICATED_PROXY", "true")

	reg := providers.NewRegistry()
	reg.Register(stubProvider{})

	ks := repository.NewKeyStore()
	return httpserver.NewRouter(reg, ks, nil, nil, gw, nil, rlStore, nil, nil, nil, "", nil)
}

func newProbeTestGateway(t *testing.T) *aigateway.Gateway {
	t.Helper()
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "stub"}},
	})

	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}
	gw.RegisterProvider(stubProvider{})
	return gw
}

// sameIPRequest builds a request that always resolves to the same source IP,
// so repeated calls land in the same rate-limit bucket.
func sameIPRequest(method, path string) *http.Request {
	req := httptest.NewRequestWithContext(context.Background(), method, path, nil)
	req.RemoteAddr = "203.0.113.10:5555"
	return req
}

// TestProbes_SurviveClientRateLimitExhaustion pins the fix: a burst of client
// traffic that exhausts the per-IP rate-limit bucket must not also 429 an
// orchestrator's /livez or /health check from that same source IP (e.g.
// behind a shared load balancer) -- that would turn a load spike into a
// kubelet-driven restart loop.
func TestProbes_SurviveClientRateLimitExhaustion(t *testing.T) {
	gw := newProbeTestGateway(t)
	rlStore := ratelimit.NewStore(1, 1) // 1 rps, burst 1 -- exhausted by the 2nd request
	router := buildTestRouterWithRateLimit(t, gw, rlStore)

	do := func(req *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	if w := do(sameIPRequest(http.MethodGet, "/v1/models")); w.Code != http.StatusOK {
		t.Fatalf("first /v1/models = %d, want 200: %s", w.Code, w.Body.String())
	}

	// The client bucket for this source IP is now exhausted: /v1/* must 429.
	if w := do(sameIPRequest(http.MethodGet, "/v1/models")); w.Code != http.StatusTooManyRequests {
		t.Fatalf("second /v1/models = %d, want 429 (client bucket should be exhausted): %s", w.Code, w.Body.String())
	}

	// Health probes from the SAME exhausted source IP must still succeed.
	if w := do(sameIPRequest(http.MethodGet, "/livez")); w.Code != http.StatusOK {
		t.Fatalf("/livez after client-bucket exhaustion = %d, want 200: %s", w.Code, w.Body.String())
	}
	if w := do(sameIPRequest(http.MethodGet, "/health")); w.Code != http.StatusOK {
		t.Fatalf("/health after client-bucket exhaustion = %d, want 200: %s", w.Code, w.Body.String())
	}
}

// TestReadyz_ExemptFromClientRateLimit verifies /readyz is not gated by the
// shared per-client bucket, so it is never coupled to unrelated /v1/* traffic.
func TestReadyz_ExemptFromClientRateLimit(t *testing.T) {
	gw := newProbeTestGateway(t)
	rlStore := ratelimit.NewStore(1, 1)
	router := buildTestRouterWithRateLimit(t, gw, rlStore)

	do := func(req *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	do(sameIPRequest(http.MethodGet, "/v1/models")) // consumes the only token
	// Assert the bucket really is exhausted. Without this the /readyz check below
	// would still pass if the client limiter silently stopped applying at all.
	if w := do(sameIPRequest(http.MethodGet, "/v1/models")); w.Code != http.StatusTooManyRequests {
		t.Fatalf("second /v1/models = %d, want 429: the client bucket is not exhausted, so this test proves nothing", w.Code)
	}
	if w := do(sameIPRequest(http.MethodGet, "/readyz")); w.Code != http.StatusOK {
		t.Fatalf("/readyz after client-bucket exhaustion = %d, want 200: %s", w.Code, w.Body.String())
	}
}

// TestReadyz_IsNeverShedForVolume is the router-level regression test for the
// availability finding. /readyz used to carry its own process-global token
// bucket, so any unauthenticated caller could drain it and every orchestrator
// probe that followed got a 429 -- read as "not ready", removing the instance
// from service, while /livez stayed 200 so it was never restarted either.
//
// The Ping() fan-out those calls make is still bounded, but by the handler's
// short-lived result cache (internal/handler/health.go) rather than by refusing
// requests. A readiness probe therefore always gets an answer.
func TestReadyz_IsNeverShedForVolume(t *testing.T) {
	gw := newProbeTestGateway(t)
	router := buildTestRouterWithRateLimit(t, gw, nil) // client limiter disabled entirely

	for i := range 200 {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, sameIPRequest(http.MethodGet, "/readyz"))
		if w.Code != http.StatusOK {
			t.Fatalf("/readyz call %d = %d, want 200: a readiness probe must never be refused "+
				"for request volume: %s", i, w.Code, w.Body.String())
		}
	}
}

// TestReadyz_IsNeverShedAcrossManySourceAddresses is the same assertion for a
// caller that varies its source address, which is what a real attempt to drain
// a shared bucket looks like.
func TestReadyz_IsNeverShedAcrossManySourceAddresses(t *testing.T) {
	gw := newProbeTestGateway(t)
	router := buildTestRouterWithRateLimit(t, gw, nil) // client limiter disabled entirely

	for i := range 200 {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/readyz", nil)
		req.RemoteAddr = fmt.Sprintf("198.51.100.%d:5555", i%256)

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("/readyz call %d from %s = %d, want 200: %s", i, req.RemoteAddr, w.Code, w.Body.String())
		}
	}
}
