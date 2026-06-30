package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/ratelimit"
)

const testIP = "1.2.3.4:1234"

func makeRateLimitHandler(store *ratelimit.Store) http.Handler {
	return RateLimit(store)(dummyHandler)
}

func TestRateLimit_AllowsRequestUnderLimit(t *testing.T) {
	store := ratelimit.NewStore(10, 10)
	handler := makeRateLimitHandler(store)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = testIP
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestRateLimit_Blocks_WhenBurstExceeded(t *testing.T) {
	// burst=1: first request consumes the only token, second is rejected.
	store := ratelimit.NewStore(1, 1)
	handler := makeRateLimitHandler(store)

	ip := testIP

	first := httptest.NewRequest(http.MethodGet, "/", nil)
	first.RemoteAddr = ip
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, first)
	if w1.Code != http.StatusOK {
		t.Fatalf("first request: expected 200, got %d", w1.Code)
	}

	second := httptest.NewRequest(http.MethodGet, "/", nil)
	second.RemoteAddr = ip
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, second)
	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: expected 429, got %d", w2.Code)
	}
}

func TestRateLimit_Returns429WithOpenAIErrorBody(t *testing.T) {
	store := ratelimit.NewStore(1, 1)
	handler := makeRateLimitHandler(store)

	ip := testIP

	// Exhaust the bucket.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = ip
	handler.ServeHTTP(httptest.NewRecorder(), r)

	// Blocked request — check content type and body shape.
	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	r2.RemoteAddr = ip
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r2)

	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json, got %q", ct)
	}
	body := w.Body.String()
	for _, want := range []string{`"error"`, `"rate_limit_error"`, `"rate_limit_exceeded"`} {
		if !strings.Contains(body, want) {
			t.Errorf("response body missing %q: %s", want, body)
		}
	}
}

func TestRateLimit_UsesResolvedRemoteAddr(t *testing.T) {
	// burst=1: the rate limiter keys on the resolved r.RemoteAddr (host only).
	// RealIPMiddleware is responsible for resolving forwarded headers into
	// r.RemoteAddr; this test verifies the rate limiter uses that resolved value.
	store := ratelimit.NewStore(1, 1)
	handler := makeRateLimitHandler(store)

	makeReq := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		// Simulate what RealIPMiddleware would produce: bare host, no port.
		r.RemoteAddr = "203.0.113.5"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	if makeReq().Code != http.StatusOK {
		t.Fatal("first request should be allowed")
	}
	if makeReq().Code != http.StatusTooManyRequests {
		t.Fatal("second request should be rate-limited (same resolved IP)")
	}
}

func TestRateLimit_DifferentIPs_IndependentBuckets(t *testing.T) {
	// burst=1: each IP gets its own bucket so both first requests pass.
	store := ratelimit.NewStore(1, 1)
	handler := makeRateLimitHandler(store)

	for _, ip := range []string{"1.1.1.1:1", "2.2.2.2:2"} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = ip
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("IP %s: expected 200, got %d", ip, w.Code)
		}
	}
}

func TestRateLimit_NilStore_Passthrough(t *testing.T) {
	// A nil store means no rate-limit store is configured; the middleware must
	// be a transparent passthrough rather than panic-at-request-time.
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	RateLimit(nil)(next).ServeHTTP(
		httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/", nil),
	)
	if !called {
		t.Fatal("nil store: expected next handler to be called (passthrough), but it was not")
	}
}

func TestRateLimit_NextHandler_NotCalledOnBlock(t *testing.T) {
	store := ratelimit.NewStore(1, 1)
	called := false
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true })
	handler := RateLimit(store)(next)

	ip := "5.5.5.5:5"

	// First request passes — next is called.
	r1 := httptest.NewRequest(http.MethodGet, "/", nil)
	r1.RemoteAddr = ip
	handler.ServeHTTP(httptest.NewRecorder(), r1)
	if !called {
		t.Fatal("next should be called on first (allowed) request")
	}

	// Second request is blocked — next must not be called again.
	called = false
	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	r2.RemoteAddr = ip
	handler.ServeHTTP(httptest.NewRecorder(), r2)
	if called {
		t.Fatal("next must not be called when request is rate-limited")
	}
}
