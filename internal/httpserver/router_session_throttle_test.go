package httpserver_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/admin/repository"
	"github.com/ferro-labs/ai-gateway/internal/httpserver"
	"github.com/ferro-labs/ai-gateway/providers"
)

// POST /admin/session is the only unauthenticated write path on the gateway,
// and until it carried its own limiter the only bound on it was the general
// per-IP one — which shares a bucket with /v1 traffic and disappears entirely
// when an operator sets RATE_LIMIT_RPS=0 to tune inference throughput.
//
// Every router in these tests is built with a nil rate-limit store, which is
// exactly that configuration, so the throttle asserted here is the endpoint's
// own or there is none.
func newThrottleRouter(t *testing.T) http.Handler {
	t.Helper()
	reg := providers.NewRegistry()
	reg.Register(stubProvider{})
	return httpserver.NewRouter(reg, repository.NewKeyStore(), repository.NewSessionStore(), nil, nil, nil, nil, nil, nil, nil, "", nil)
}

// signIn presents a credential from addr and reports the status.
func signIn(t *testing.T, router http.Handler, addr, credential string) int {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/admin/session", nil)
	req.Header.Set("Authorization", "Bearer "+credential)
	req.RemoteAddr = addr + ":54321"
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w.Code
}

func TestSessionEndpoint_ThrottlesRepeatedAttempts(t *testing.T) {
	router := newThrottleRouter(t)
	const addr = "192.0.2.10"

	// Drain the burst. Each attempt is rejected on its merits (401), which is
	// the point: a wrong credential must still cost a token, or guessing is
	// free.
	var throttledAt int
	for attempt := 1; attempt <= 200; attempt++ {
		code := signIn(t, router, addr, fmt.Sprintf("wrong-key-%d", attempt))
		if code == http.StatusTooManyRequests {
			throttledAt = attempt
			break
		}
		if code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want 401 or 429", attempt, code)
		}
	}

	if throttledAt == 0 {
		t.Fatal("200 sign-in attempts from one address were never throttled; " +
			"the endpoint has no bound of its own")
	}
	// The allowance is deliberately generous enough for a whole team behind one
	// shared egress address to sign in at once. Assert the shape, not the exact
	// constant, so tuning it does not break the test — but catch a value so
	// small it would lock out a normal office.
	if throttledAt < 10 {
		t.Fatalf("throttled after only %d attempts; too tight for operators behind a shared address", throttledAt)
	}
}

func TestSessionEndpoint_ThrottlesPerAddress(t *testing.T) {
	router := newThrottleRouter(t)

	// Exhaust one address.
	var exhausted bool
	for attempt := 1; attempt <= 200; attempt++ {
		if signIn(t, router, "192.0.2.20", fmt.Sprintf("wrong-key-%d", attempt)) == http.StatusTooManyRequests {
			exhausted = true
			break
		}
	}
	if !exhausted {
		t.Fatal("could not exhaust the first address; the throttle is not in place")
	}

	// A different operator must be unaffected. A shared bucket would turn one
	// attacker into an outage for everyone else.
	if code := signIn(t, router, "192.0.2.21", "wrong-key"); code != http.StatusUnauthorized {
		t.Fatalf("second address = %d, want 401; one address's attempts exhausted another's allowance", code)
	}
}
