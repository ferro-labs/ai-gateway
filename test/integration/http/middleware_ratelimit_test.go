//go:build integration
// +build integration

package http_test

import (
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
)

func TestMiddlewareRateLimit_ExceedLimit_Returns429(t *testing.T) {
	// Configure rate limit: 1 RPS, burst 1.
	// The first request consumes the burst token; subsequent rapid requests
	// should be rejected before the bucket refills.
	//
	// Probe routes (/health, /livez, /readyz) are deliberately mounted outside the
	// per-client limiter, so this must drive a rate-limited route to observe a 429.
	env := newTestServer(t, withRateLimit(1, 1))

	// Fire 10 concurrent requests from the same "IP" — at least some must get 429.
	// We set X-Forwarded-For to force a consistent key (by default, httptest
	// connections use different ephemeral ports in RemoteAddr).
	var wg sync.WaitGroup
	var count429 atomic.Int32
	var count200 atomic.Int32

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("GET", env.Server.URL+"/v1/models", nil)
			req.Header.Set("X-Forwarded-For", "10.0.0.1")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return
			}
			resp.Body.Close()
			switch resp.StatusCode {
			case http.StatusTooManyRequests:
				count429.Add(1)
			case http.StatusOK:
				count200.Add(1)
			}
		}()
	}
	wg.Wait()

	if count429.Load() == 0 {
		t.Fatalf("expected at least one 429 response, got %d OK and %d 429", count200.Load(), count429.Load())
	}
	t.Logf("rate limit results: %d OK, %d 429", count200.Load(), count429.Load())
}

func TestMiddlewareRateLimit_NotEnabled_NoRejection(t *testing.T) {
	// Default server with no rate limiter — all requests should pass.
	// Drives a rate-limited route, not a probe: a probe would pass here even with
	// a limiter installed, so it could not tell the two configurations apart.
	env := newTestServer(t)

	for i := 0; i < 10; i++ {
		req, _ := http.NewRequest("GET", env.Server.URL+"/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+testMasterKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i, resp.StatusCode)
		}
	}
}
