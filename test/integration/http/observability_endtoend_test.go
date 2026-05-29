//go:build integration
// +build integration

package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gwotel "github.com/ferro-labs/ai-gateway/internal/otel"
	"github.com/ferro-labs/ai-gateway/observability"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// captureExporter is an in-memory observability.Exporter used to assert
// that the gateway's event pathway (Provider.RecordEvent → Exporter.Export)
// is wired end-to-end through the real HTTP router.
type captureExporter struct {
	name string

	mu       sync.Mutex
	events   []observability.Event
	inits    int
	shutdown int
}

func (c *captureExporter) Name() string { return c.name }

func (c *captureExporter) Init(_ context.Context, _ map[string]any) error {
	c.mu.Lock()
	c.inits++
	c.mu.Unlock()
	return nil
}

func (c *captureExporter) Export(_ context.Context, e observability.Event) error {
	c.mu.Lock()
	c.events = append(c.events, e)
	c.mu.Unlock()
	return nil
}

func (c *captureExporter) Shutdown(_ context.Context) error {
	c.mu.Lock()
	c.shutdown++
	c.mu.Unlock()
	return nil
}

func (c *captureExporter) snapshot() []observability.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]observability.Event(nil), c.events...)
}

// waitForEvent polls until an event matching pred is captured or the
// deadline elapses. Streaming events are recorded from the streamwrap
// finisher goroutine, so a short poll is needed after the HTTP response.
func (c *captureExporter) waitForEvent(t *testing.T, timeout time.Duration, pred func(observability.Event) bool) observability.Event {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, e := range c.snapshot() {
			if pred(e) {
				return e
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no captured event matched within %s; captured=%d", timeout, len(c.snapshot()))
	return observability.Event{}
}

// exporterSeq guarantees a unique exporter-registry name per registration
// (RegisterExporter panics on duplicates; the registry has no unregister).
var exporterSeq int64

// installObservability builds a real observability.Provider via
// gwotel.Init with a capturing exporter attached and installs it on the
// gateway. When endpoint is non-empty the full OTLP pipeline + W3C
// propagator are wired (required for inbound-traceparent extraction);
// when empty, a no-op tracer is used but the exporter event pathway still
// fires. The capturing exporter is returned for assertions.
func installObservability(t *testing.T, env *testEnv, endpoint string) *captureExporter {
	t.Helper()

	ce := &captureExporter{name: fmt.Sprintf("itest-capture-%d", atomic.AddInt64(&exporterSeq, 1))}
	observability.RegisterExporter(ce.name, func() observability.Exporter { return ce })

	cfg := gwotel.Config{
		Enabled:       true,
		Endpoint:      endpoint,
		Protocol:      "http/protobuf",
		ServiceName:   "ferrogw-obs-itest",
		SampleRatio:   1.0,
		PrivacyLevel:  gwotel.PrivacyLevelMetadata,
		ShutdownGrace: 5 * time.Second,
		Exporters:     []gwotel.ExporterConfig{{Name: ce.name, Enabled: true}},
	}

	initCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	prov, shutdown, err := gwotel.Init(initCtx, cfg)
	if err != nil {
		t.Fatalf("gwotel.Init: %v", err)
	}
	env.Gateway.SetObservability(prov)
	t.Cleanup(func() {
		shutdownCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = shutdown(shutdownCtx)
	})
	return ce
}

func chatRequest(t *testing.T, env *testEnv, hdrs map[string]string, stream bool) *http.Response {
	t.Helper()
	body := map[string]any{
		"model":    stubModelName,
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
		"stream":   stream,
	}
	payload, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, env.Server.URL+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	for k, v := range hdrs {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

// TestObservability_TraceIDUnification_InboundTraceparent proves that an
// inbound W3C traceparent's trace_id flows all the way through the real
// router: it becomes the X-Request-ID response header AND the TraceID on
// the exported event — the unification guarantee, verified end-to-end.
func TestObservability_TraceIDUnification_InboundTraceparent(t *testing.T) {
	rec := &otlpReceiver{}
	collector := httptest.NewServer(rec.handler())
	// Registered before installObservability so the provider's shutdown
	// flush (a later t.Cleanup) drains to a still-open collector first.
	t.Cleanup(collector.Close)

	env := newTestServer(t)
	ce := installObservability(t, env, strings.TrimPrefix(collector.URL, "http://"))

	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	resp := chatRequest(t, env, map[string]string{
		"traceparent": "00-" + traceID + "-00f067aa0ba902b7-01",
	}, false)
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	if got := resp.Header.Get("X-Request-ID"); got != traceID {
		t.Errorf("X-Request-ID = %q, want inbound trace_id %q", got, traceID)
	}

	evt := ce.waitForEvent(t, 2*time.Second, func(e observability.Event) bool {
		return e.Subject == "gateway.request.completed"
	})
	if evt.TraceID != traceID {
		t.Errorf("event TraceID = %q, want inbound trace_id %q (unification broken)", evt.TraceID, traceID)
	}
}

// TestObservability_TraceIDUnification_SelfOriginated proves that for a
// request with no inbound traceparent, the generated request ID is shared
// between the X-Request-ID response header and the exported event TraceID.
func TestObservability_TraceIDUnification_SelfOriginated(t *testing.T) {
	rec := &otlpReceiver{}
	collector := httptest.NewServer(rec.handler())
	t.Cleanup(collector.Close)

	env := newTestServer(t)
	ce := installObservability(t, env, strings.TrimPrefix(collector.URL, "http://"))

	resp := chatRequest(t, env, nil, false)
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	reqID := resp.Header.Get("X-Request-ID")
	if len(reqID) != 32 {
		t.Fatalf("X-Request-ID = %q, want a generated 32-hex trace ID", reqID)
	}

	evt := ce.waitForEvent(t, 2*time.Second, func(e observability.Event) bool {
		return e.Subject == "gateway.request.completed"
	})
	if evt.TraceID != reqID {
		t.Errorf("event TraceID = %q, want X-Request-ID %q (unification broken)", evt.TraceID, reqID)
	}
}

// TestObservability_ExporterReceivesCompletedEvent proves the exporter
// event pathway is wired end-to-end for a successful non-streaming request,
// without requiring an OTLP endpoint (exporters-only provider).
func TestObservability_ExporterReceivesCompletedEvent(t *testing.T) {
	env := newTestServer(t)
	ce := installObservability(t, env, "") // no endpoint — exporter-only path

	resp := chatRequest(t, env, nil, false)
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	evt := ce.waitForEvent(t, 2*time.Second, func(e observability.Event) bool {
		return e.Subject == "gateway.request.completed"
	})
	if evt.Provider != "stub" {
		t.Errorf("event Provider = %q, want %q", evt.Provider, "stub")
	}
	if evt.Model != stubModelName {
		t.Errorf("event Model = %q, want %q", evt.Model, stubModelName)
	}
	if evt.Status != http.StatusOK {
		t.Errorf("event Status = %d, want 200", evt.Status)
	}
	if evt.Stream {
		t.Errorf("event Stream = true, want false")
	}
	if evt.TokensIn != 10 || evt.TokensOut != 5 {
		t.Errorf("event tokens = (%d,%d), want (10,5)", evt.TokensIn, evt.TokensOut)
	}
}

// TestObservability_ExporterReceivesFailedEvent proves a provider failure
// produces a gateway.request.failed event with a non-empty (redacted)
// error message.
func TestObservability_ExporterReceivesFailedEvent(t *testing.T) {
	env := newTestServer(t)
	env.Stub.CompleteHook = func(_ context.Context, _ core.Request) (*core.Response, error) {
		return nil, fmt.Errorf("stub upstream error (500): boom")
	}
	ce := installObservability(t, env, "")

	resp := chatRequest(t, env, nil, false)
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 500 {
		t.Fatalf("expected a 5xx status for provider failure, got %d", resp.StatusCode)
	}

	evt := ce.waitForEvent(t, 2*time.Second, func(e observability.Event) bool {
		return e.Subject == "gateway.request.failed"
	})
	if evt.Error == "" {
		t.Errorf("failed event Error is empty, want a (redacted) message")
	}
	if evt.Status < 500 {
		t.Errorf("failed event Status = %d, want >= 500", evt.Status)
	}
}

// TestObservability_ExporterReceivesStreamingEvent proves the streaming
// path also emits a completed event (recorded from the streamwrap finisher
// once the channel drains) with Stream=true.
func TestObservability_ExporterReceivesStreamingEvent(t *testing.T) {
	env := newTestServer(t)
	ce := installObservability(t, env, "")

	resp := chatRequest(t, env, nil, true)
	// Drain the SSE stream fully so the finisher runs.
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	evt := ce.waitForEvent(t, 2*time.Second, func(e observability.Event) bool {
		return e.Subject == "gateway.request.completed" && e.Stream
	})
	if evt.Provider != "stub" {
		t.Errorf("streaming event Provider = %q, want %q", evt.Provider, "stub")
	}
	if !evt.Stream {
		t.Errorf("streaming event Stream = false, want true")
	}
}
