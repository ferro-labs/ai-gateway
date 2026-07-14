package httpserver_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/admin"
	"github.com/ferro-labs/ai-gateway/internal/httpserver"
	"github.com/ferro-labs/ai-gateway/internal/ratelimit"
	"github.com/ferro-labs/ai-gateway/providers"
)

// buildTestRouterWithRateLimit is like buildTestRouter but wires a real
// per-client rate limiter so a test can drive it to exhaustion.
func buildTestRouterWithRateLimit(t *testing.T, gw *aigateway.Gateway, rlStore *ratelimit.Store) http.Handler {
	t.Helper()
	t.Setenv("ALLOW_UNAUTHENTICATED_PROXY", "true")

	reg := providers.NewRegistry()
	reg.Register(stubProvider{})

	ks := admin.NewKeyStore()
	return httpserver.NewRouter(reg, ks, nil, gw, nil, rlStore, nil, nil, "", nil)
}

func newProbeTestGateway(t *testing.T) *aigateway.Gateway {
	t.Helper()
	gw, err := aigateway.New(aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: "stub"}},
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
// shared per-client bucket either -- it keeps its own dedicated limit instead
// of being coupled to unrelated /v1/* traffic.
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

// TestReadyz_HasOwnDedicatedRateLimit verifies /readyz is not fully exempted
// from rate limiting: it fans out to Ping() calls against the key store and
// config manager (internal/handler/health.go), so hammering it directly must
// still trip a dedicated, /readyz-only limit rather than allow unbounded
// concurrent DB pings from an unauthenticated caller.
func TestReadyz_HasOwnDedicatedRateLimit(t *testing.T) {
	gw := newProbeTestGateway(t)
	router := buildTestRouterWithRateLimit(t, gw, nil) // client limiter disabled entirely

	saw429 := false
	for i := 0; i < 50; i++ {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, sameIPRequest(http.MethodGet, "/readyz"))
		if w.Code == http.StatusTooManyRequests {
			saw429 = true
			break
		}
		if w.Code != http.StatusOK {
			t.Fatalf("/readyz call %d = %d, want 200 or 429: %s", i, w.Code, w.Body.String())
		}
	}
	if !saw429 {
		t.Fatal("/readyz never 429d after 50 rapid calls from one IP -- it has no dedicated rate limit")
	}
}

// TestReadyz_RateLimitIsGlobalNotPerSource is the assertion the single-IP test
// above cannot make. /readyz is unauthenticated and every call fans out to
// Ping() against the backing stores, so the ceiling that matters is the total
// rate this process will serve -- a property of the stores, not of any one
// caller. A per-IP bucket cannot express that: each new source address is handed
// a fresh allowance, so a caller that varies its address multiplies the ceiling
// and the store's per-key map grows with every address it presents.
func TestReadyz_RateLimitIsGlobalNotPerSource(t *testing.T) {
	gw := newProbeTestGateway(t)
	router := buildTestRouterWithRateLimit(t, gw, nil) // client limiter disabled entirely

	// Every request arrives from a DIFFERENT source address. Under a per-IP
	// bucket each would get its own allowance and none would ever be shed.
	saw429 := false
	for i := range 60 {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/readyz", nil)
		req.RemoteAddr = fmt.Sprintf("198.51.100.%d:5555", i%256)

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code == http.StatusTooManyRequests {
			saw429 = true
			break
		}
		if w.Code != http.StatusOK {
			t.Fatalf("/readyz call %d = %d, want 200 or 429: %s", i, w.Code, w.Body.String())
		}
	}
	if !saw429 {
		t.Fatal("/readyz served 60 rapid calls from 60 distinct source addresses without shedding: " +
			"the limit is per-source, so it does not bound the Ping() fan-out at all")
	}
}
