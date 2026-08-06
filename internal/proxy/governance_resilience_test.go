package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/config"
)

// failingUpstream answers every request with a 500 and counts the attempts.
func failingUpstream(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// gatewayWithTarget builds a gateway whose single target carries the supplied
// resilience settings.
func gatewayWithTarget(t *testing.T, upstream string, target config.Target) *aigateway.Gateway {
	t.Helper()
	t.Setenv("FERRO_MODEL_CATALOG_TIMEOUT", "0")

	target.VirtualKey = "stub"
	target.Models = []string{"stub-model"}
	gw, err := aigateway.New(config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{target},
	})
	if err != nil {
		t.Fatalf("aigateway.New: %v", err)
	}
	t.Cleanup(func() { _ = gw.Close() })
	gw.RegisterProvider(&proxiableStub{name: "stub", baseURL: upstream, models: []string{"stub-model"}})
	if err := gw.LoadPlugins(); err != nil {
		t.Fatalf("LoadPlugins: %v", err)
	}
	return gw
}

// Retry is excluded from the pass-through DELIBERATELY, so this asserts it —
// a configured targets[].retry must not replay a forward. The body has already
// been streamed upstream by the time a failure is known, and the endpoints
// behind this surface are not idempotent: a retried /v1/files upload is a
// second file.
func TestPassthrough_DoesNotRetry(t *testing.T) {
	up, hits := failingUpstream(t)
	gw := gatewayWithTarget(t, up.URL, config.Target{
		Retry: &config.RetryConfig{Attempts: 3, InitialBackoffMs: 1},
	})

	w := passthroughPOST(t, gw, "/v1/responses", `{"model":"stub-model","input":"hi"}`, nil)

	// The upstream's own 500 reaches the client: the pass-through relays what
	// the provider said rather than replacing it.
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (the upstream's own answer, relayed)", w.Code)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("upstream received %d attempts, want exactly 1 — retry must not apply to a pass-through", got)
	}
}

// The circuit breaker applies per target: a failing upstream trips it, and the
// next request is refused without an upstream call.
func TestPassthrough_CircuitBreakerApplies(t *testing.T) {
	up, hits := failingUpstream(t)
	gw := gatewayWithTarget(t, up.URL, config.Target{
		CircuitBreaker: &config.CircuitBreakerConfig{FailureThreshold: 1, Timeout: "30s"},
	})

	body := `{"model":"stub-model","input":"hi"}`
	if w := passthroughPOST(t, gw, "/v1/responses", body, nil); w.Code != http.StatusInternalServerError {
		t.Fatalf("first request: status = %d, want 500", w.Code)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("upstream attempts after first request = %d, want 1", got)
	}

	w := passthroughPOST(t, gw, "/v1/responses", body, nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("second request: status = %d, want 503 (circuit open); body: %s", w.Code, w.Body.String())
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("upstream received %d attempts, want 1 — the open circuit must refuse without calling upstream", got)
	}
}

// A 4xx from the upstream is the caller's fault, not the target's, so it must
// not trip a breaker shared by every other caller.
func TestPassthrough_ClientErrorDoesNotTripTheBreaker(t *testing.T) {
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(up.Close)

	gw := gatewayWithTarget(t, up.URL, config.Target{
		CircuitBreaker: &config.CircuitBreakerConfig{FailureThreshold: 1, Timeout: "30s"},
	})

	body := `{"model":"stub-model","input":"hi"}`
	for i := range 2 {
		if w := passthroughPOST(t, gw, "/v1/responses", body, nil); w.Code != http.StatusBadRequest {
			t.Fatalf("request %d: status = %d, want 400", i+1, w.Code)
		}
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("upstream received %d requests, want 2 — a 4xx must not open the breaker", got)
	}
}

// Per-target concurrency applies: with one slot, the pass-through never has two
// forwards in flight against the target at once.
//
// Asserted as observed concurrency rather than as a shed request, because a
// queued request and a shed one are both correct outcomes here and which one a
// given request gets depends on scheduling. What must never happen — and what
// happened before this — is a forward that consulted no limiter at all.
func TestPassthrough_ConcurrencyLimitApplies(t *testing.T) {
	var inFlight, maxInFlight atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		now := inFlight.Add(1)
		for {
			peak := maxInFlight.Load()
			if now <= peak || maxInFlight.CompareAndSwap(peak, now) {
				break
			}
		}
		// Long enough that unlimited forwards would overlap; the limiter is what
		// stops them.
		time.Sleep(30 * time.Millisecond)
		inFlight.Add(-1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(up.Close)

	gw := gatewayWithTarget(t, up.URL, config.Target{
		Concurrency: &config.ConcurrencyConfig{MaxConcurrency: 1, QueueSize: 8},
	})

	body := `{"model":"stub-model","input":"hi"}`
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/responses", strings.NewReader(body))
			Handler(gw)(httptest.NewRecorder(), r)
		}()
	}
	wg.Wait()

	if peak := maxInFlight.Load(); peak > 1 {
		t.Errorf("peak concurrent forwards = %d, want 1 — the per-target limiter was not applied", peak)
	}
}
