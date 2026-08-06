package handler

import (
	"context"
	"errors"
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

// countingPinger records how many times the readiness path actually reached the
// backing store, and can be flipped to failing mid-test.
type countingPinger struct {
	calls  atomic.Int64
	failed atomic.Bool
}

func (c *countingPinger) Ping(context.Context) error {
	c.calls.Add(1)
	if c.failed.Load() {
		return errors.New("connection refused")
	}
	return nil
}

// blockingPinger blocks until released, so a test can hold one evaluation open
// while other callers arrive.
type blockingPinger struct {
	calls   atomic.Int64
	entered chan struct{}
	release chan struct{}
}

func (b *blockingPinger) Ping(context.Context) error {
	b.calls.Add(1)
	b.entered <- struct{}{}
	<-b.release
	return nil
}

func newReadyzTestGateway(t *testing.T) *aigateway.Gateway {
	t.Helper()
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "health-provider"}},
	})
	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}
	gw.RegisterProvider(healthProvider{})
	return gw
}

// fakeClock is a manually advanced clock, so cache-staleness tests never sleep.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func probe(t *testing.T, h http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// TestReadyz_ConcurrentProbesAllAnsweredFromOneEvaluation is the regression test
// for the availability finding: /readyz used to be gated by a process-global
// token bucket, so an unauthenticated caller could exhaust it and every
// subsequent orchestrator probe got a 429 -- which an orchestrator reads as "not
// ready" and removes the instance from service, while /livez kept returning 200
// so the process was never restarted either.
//
// The two properties that must now hold at the same time: every probe is
// answered (never shed), and the Ping() fan-out at the backing stores stays
// bounded no matter how many probes arrive.
func TestReadyz_ConcurrentProbesAllAnsweredFromOneEvaluation(t *testing.T) {
	gw := newReadyzTestGateway(t)
	pinger := &countingPinger{}
	clock := &fakeClock{t: time.Now()}
	h := readyz(gw, []Pinger{pinger}, clock.now)

	const probes = 200
	var wg sync.WaitGroup
	codes := make([]int, probes)
	for i := range probes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = probe(t, h).Code
		}()
	}
	wg.Wait()

	for i, code := range codes {
		if code != http.StatusOK {
			t.Fatalf("probe %d = %d, want 200: a readiness probe must never be refused for volume", i, code)
		}
	}
	if got := pinger.calls.Load(); got != 1 {
		t.Fatalf("store pings = %d, want 1: %d probes inside one TTL must share a single evaluation", got, probes)
	}
}

// TestReadyz_BoundsPingFanoutByTimeNotRequestCount pins the ceiling that
// replaces the old rate limit: the dependency load is a function of elapsed
// time, not of how many callers ask.
func TestReadyz_BoundsPingFanoutByTimeNotRequestCount(t *testing.T) {
	gw := newReadyzTestGateway(t)
	pinger := &countingPinger{}
	clock := &fakeClock{t: time.Now()}
	h := readyz(gw, []Pinger{pinger}, clock.now)

	// Three TTL windows, a hundred probes in each.
	for window := range 3 {
		for range 100 {
			if code := probe(t, h).Code; code != http.StatusOK {
				t.Fatalf("window %d: probe = %d, want 200", window, code)
			}
		}
		clock.advance(readyzCacheTTL)
	}

	if got := pinger.calls.Load(); got != 3 {
		t.Fatalf("store pings = %d, want 3 (one per TTL window across 300 probes)", got)
	}
}

// TestReadyz_ReportsUnreadyOnTheNextProbeAfterTTL is the staleness bound. A
// cached answer is only useful if a genuine transition to unready is still
// reported promptly -- within one TTL, which is at or below the fastest probe
// cadence an orchestrator can be configured for.
func TestReadyz_ReportsUnreadyOnTheNextProbeAfterTTL(t *testing.T) {
	gw := newReadyzTestGateway(t)
	pinger := &countingPinger{}
	clock := &fakeClock{t: time.Now()}
	h := readyz(gw, []Pinger{pinger}, clock.now)

	if code := probe(t, h).Code; code != http.StatusOK {
		t.Fatalf("initial probe = %d, want 200", code)
	}

	pinger.failed.Store(true)
	clock.advance(readyzCacheTTL)

	w := probe(t, h)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("probe after the store went down = %d, want 503: %s", w.Code, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, "store unreachable") {
		t.Fatalf("503 body = %s, want the store-unreachable reason", body)
	}
}

// TestReadyz_RecoversOnTheNextProbeAfterTTL is the other half of the staleness
// bound: a cached not_ready must not keep an instance out of rotation once the
// dependency comes back.
func TestReadyz_RecoversOnTheNextProbeAfterTTL(t *testing.T) {
	gw := newReadyzTestGateway(t)
	pinger := &countingPinger{}
	pinger.failed.Store(true)
	clock := &fakeClock{t: time.Now()}
	h := readyz(gw, []Pinger{pinger}, clock.now)

	if code := probe(t, h).Code; code != http.StatusServiceUnavailable {
		t.Fatalf("initial probe = %d, want 503", code)
	}

	pinger.failed.Store(false)
	clock.advance(readyzCacheTTL)

	if code := probe(t, h).Code; code != http.StatusOK {
		t.Fatalf("probe after recovery = %d, want 200", code)
	}
}

// TestReadyz_ClientDisconnectDoesNotAbortTheSharedEvaluation covers the hazard
// that comes with sharing one evaluation between callers: the caller that
// happens to trigger it must not be able to cancel it for everyone by hanging
// up. An orchestrator probe with a 1s timeout routinely disconnects.
func TestReadyz_ClientDisconnectDoesNotAbortTheSharedEvaluation(t *testing.T) {
	gw := newReadyzTestGateway(t)
	pinger := &countingPinger{}
	clock := &fakeClock{t: time.Now()}
	h := readyz(gw, []Pinger{pinger}, clock.now)

	ctx, cancel := context.WithCancel(t.Context())
	cancel() // the client is already gone before the handler runs
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("probe from a disconnected client = %d, want 200: a cancelled request context "+
			"must not fail the evaluation every other caller is served from: %s", w.Code, w.Body.String())
	}
}

// TestReadyz_LateArrivalsWaitForTheInFlightEvaluation asserts that callers
// arriving while an evaluation is running are served from it rather than each
// starting one of their own.
func TestReadyz_LateArrivalsWaitForTheInFlightEvaluation(t *testing.T) {
	gw := newReadyzTestGateway(t)
	// entered is buffered so that a regression which lets every caller ping
	// fails the call-count assertion below instead of deadlocking the test.
	pinger := &blockingPinger{entered: make(chan struct{}, 16), release: make(chan struct{})}
	clock := &fakeClock{t: time.Now()}
	h := readyz(gw, []Pinger{pinger}, clock.now)

	first := make(chan int, 1)
	go func() { first <- probe(t, h).Code }()
	<-pinger.entered // the first evaluation is now in flight and holding

	late := make(chan int, 10)
	for range 10 {
		go func() { late <- probe(t, h).Code }()
	}

	close(pinger.release)
	if code := <-first; code != http.StatusOK {
		t.Fatalf("first probe = %d, want 200", code)
	}
	for range 10 {
		if code := <-late; code != http.StatusOK {
			t.Fatalf("late probe = %d, want 200", code)
		}
	}
	if got := pinger.calls.Load(); got != 1 {
		t.Fatalf("store pings = %d, want 1: callers arriving during an evaluation must share it", got)
	}
}
