package otel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/observability"
	"go.opentelemetry.io/otel"
)

// testEndpoint is a non-routable OTLP endpoint used by Init tests; the gRPC
// client connects lazily so no live collector is required.
const testEndpoint = "localhost:4317"

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Enabled {
		t.Error("DefaultConfig should be Enabled")
	}
	if cfg.PrivacyLevel != PrivacyLevelMetadata {
		t.Errorf("DefaultConfig PrivacyLevel = %q, want %q", cfg.PrivacyLevel, PrivacyLevelMetadata)
	}
	if cfg.ServiceName != "ferrogw" {
		t.Errorf("DefaultConfig ServiceName = %q, want %q", cfg.ServiceName, "ferrogw")
	}
}

func TestInitReturnsNoOpWhenDisabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = false

	prov, shutdown, err := Init(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prov == nil {
		t.Fatal("expected non-nil Provider")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown returned error: %v", err)
	}

	// Should behave as a NoOp provider.
	_, span := prov.StartRequestSpan(context.Background(), observability.RequestAttrs{})
	if span == nil {
		t.Fatal("expected non-nil span")
	}
	span.End()
}

func TestInitReturnsNoOpWhenEndpointUnset(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Endpoint = ""

	prov, shutdown, err := Init(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prov == nil {
		t.Fatal("expected non-nil Provider")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown returned error: %v", err)
	}
}

func TestInitWithEndpointReturnsRealProvider(t *testing.T) {
	// When a real OTLP endpoint is configured, Init returns the
	// otelProvider implementation (not NoOp). The OTLP gRPC client is
	// lazy-connecting, so this test does not require a live collector.
	cfg := DefaultConfig()
	cfg.Endpoint = testEndpoint

	prov, shutdown, err := Init(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := prov.(*otelProvider); !ok {
		t.Fatalf("expected *otelProvider, got %T", prov)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_ = shutdown(ctx)
}

func TestInitRegistersGlobalTracerProvider(t *testing.T) {
	// Plugin-stage and MCP spans use the global otel.Tracer(...) API, so
	// Init must register the SDK TracerProvider globally for those child
	// spans to record. Restore whatever was installed before this test.
	prev := otel.GetTracerProvider()
	defer otel.SetTracerProvider(prev)

	cfg := DefaultConfig()
	cfg.Endpoint = testEndpoint
	cfg.ShutdownGrace = 200 * time.Millisecond // no live collector; bound the flush

	prov, shutdown, err := Init(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := prov.(*otelProvider); !ok {
		t.Fatalf("expected *otelProvider, got %T", prov)
	}

	// A span from the global tracer must record (SampleRatio 1.0 → AlwaysSample).
	_, span := otel.GetTracerProvider().Tracer("test").Start(context.Background(), "child")
	if !span.IsRecording() {
		t.Fatal("global tracer provider does not record — Init did not call SetTracerProvider")
	}
	span.End()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	// Export to the dead collector may time out; the global-provider reset
	// happens regardless, which is what we assert next.
	_ = shutdown(ctx)

	// After shutdown the global provider is reset to no-op so late spans
	// don't hit the drained pipeline.
	_, span2 := otel.GetTracerProvider().Tracer("test").Start(context.Background(), "late")
	if span2.IsRecording() {
		t.Fatal("global tracer provider not reset to no-op after shutdown")
	}
	span2.End()
}

func TestEffectiveEndpointEnvWins(t *testing.T) {
	// OTEL_EXPORTER_OTLP_ENDPOINT takes precedence over a configured endpoint
	// so container deployments can redirect telemetry by env var.
	cfg := Config{Endpoint: "http://collector:4317"}
	got := cfg.effectiveEndpoint(func(string) string { return "http://from-env:4317" })
	if got != "http://from-env:4317" {
		t.Errorf("expected env endpoint to win, got %q", got)
	}
}

func TestEffectiveEndpointFallsBackToConfig(t *testing.T) {
	// When the env var is unset, the configured endpoint is used.
	cfg := Config{Endpoint: "http://collector:4317"}
	got := cfg.effectiveEndpoint(func(string) string { return "" })
	if got != "http://collector:4317" {
		t.Errorf("expected configured endpoint, got %q", got)
	}
}

func TestEffectiveEndpointUsesEnvWhenConfigEmpty(t *testing.T) {
	cfg := Config{Endpoint: ""}
	got := cfg.effectiveEndpoint(func(k string) string {
		if k == "OTEL_EXPORTER_OTLP_ENDPOINT" {
			return "http://env-collector:4317"
		}
		return ""
	})
	if got != "http://env-collector:4317" {
		t.Errorf("expected env endpoint, got %q", got)
	}
}

func TestMiddlewarePassthrough(t *testing.T) {
	called := false
	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	h.ServeHTTP(rr, req)

	if !called {
		t.Fatal("expected wrapped handler to be invoked")
	}
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}
}

// --- Exporter pathway tests ---

// fakeExporter is a concurrency-safe test double.
type fakeExporter struct {
	mu             sync.Mutex
	initCalled     bool
	initCfg        map[string]any
	exported       []observability.Event
	shutdownCalled bool
	initErr        error
}

func (e *fakeExporter) Name() string { return "fake" }

func (e *fakeExporter) Init(_ context.Context, cfg map[string]any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.initCalled = true
	e.initCfg = cfg
	return e.initErr
}

func (e *fakeExporter) Export(_ context.Context, evt observability.Event) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.exported = append(e.exported, evt)
	return nil
}

func (e *fakeExporter) Shutdown(_ context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.shutdownCalled = true
	return nil
}

func TestInitWithExporterOnly_NotNoOp(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	// Use a test-name-scoped exporter name to avoid registration collisions
	// when tests run in parallel or are repeated by go test -count>1.
	exporterName := "fake-notnooop-" + t.Name()
	fake := &fakeExporter{}
	observability.RegisterExporter(exporterName, func() observability.Exporter { return fake })

	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Endpoint = ""
	cfg.Exporters = []ExporterConfig{{Name: exporterName, Enabled: true, Config: map[string]any{"k": "v"}}}

	prov, shutdown, err := Init(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	if prov == nil {
		t.Fatal("expected non-nil Provider")
	}

	// Must implement EventRecordingProvider.
	erp, ok := prov.(observability.EventRecordingProvider)
	if !ok {
		t.Fatalf("provider does not implement EventRecordingProvider, got %T", prov)
	}
	if !erp.RecordingEnabled() {
		t.Error("RecordingEnabled() should be true after AttachExporters")
	}

	// RecordEvent enqueues asynchronously. Call Shutdown (which drains the
	// queue before calling exporter.Shutdown) to guarantee delivery before
	// asserting that the event was received.
	evt := observability.Event{Subject: "gateway.request.completed", Provider: "openai", Status: 200}
	prov.RecordEvent(context.Background(), evt)

	// Shutdown drains remaining buffered events and then calls exporter.Shutdown.
	shutCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := shutdown(shutCtx); err != nil {
		t.Errorf("shutdown returned error: %v", err)
	}

	fake.mu.Lock()
	exported := fake.exported
	shutCalled := fake.shutdownCalled
	fake.mu.Unlock()

	if len(exported) != 1 || exported[0].Subject != "gateway.request.completed" {
		t.Errorf("exporter received unexpected events: %v", exported)
	}
	if !shutCalled {
		t.Error("exporter.Shutdown was not called")
	}
}

func TestInitWithNoEndpointAndNoExporters_IsNoOp(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Endpoint = ""
	// No exporters.

	prov, shutdown, err := Init(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The returned provider should be the NoOp (not EventRecordingProvider).
	if _, ok := prov.(observability.EventRecordingProvider); ok {
		t.Error("NoOp provider should NOT implement EventRecordingProvider")
	}

	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown returned error: %v", err)
	}
}

func TestInitExporterInitError_Skipped(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	exporterName := "fail-ex-" + t.Name()
	failEx := &fakeExporter{initErr: errors.New("bad config")}
	observability.RegisterExporter(exporterName, func() observability.Exporter { return failEx })

	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Endpoint = ""
	cfg.Exporters = []ExporterConfig{{Name: exporterName, Enabled: true}}

	// Init must not fail — the broken exporter is skipped.
	prov, shutdown, err := Init(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Init should not fail when exporter.Init errors: %v", err)
	}

	// No exporters attached → RecordingEnabled == false OR provider is NoOp.
	// Either way, no panic from RecordEvent.
	prov.RecordEvent(context.Background(), observability.Event{Subject: "test"})

	_ = shutdown(context.Background())
}

func TestInitExporterUnknownName_Skipped(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Endpoint = ""
	// Use a guaranteed-unique name that is never registered.
	cfg.Exporters = []ExporterConfig{{Name: "not-registered-" + t.Name(), Enabled: true}}

	// Init must not fail — unknown exporter is warned and skipped.
	_, shutdown, err := Init(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Init should not fail for unknown exporter: %v", err)
	}
	_ = shutdown(context.Background())
}

// --- Async dispatch focused tests (Change B) ---

// blockingExporter is a test double whose Export blocks until released.
type blockingExporter struct {
	mu             sync.Mutex
	name           string
	exported       []observability.Event
	shutdownCalled bool
	block          chan struct{} // close to unblock Export
}

func newBlockingExporter(name string) *blockingExporter {
	return &blockingExporter{name: name, block: make(chan struct{})}
}

func (e *blockingExporter) Name() string                                   { return e.name }
func (e *blockingExporter) Init(_ context.Context, _ map[string]any) error { return nil }
func (e *blockingExporter) Export(_ context.Context, evt observability.Event) error {
	<-e.block // blocks until released
	e.mu.Lock()
	e.exported = append(e.exported, evt)
	e.mu.Unlock()
	return nil
}
func (e *blockingExporter) Shutdown(_ context.Context) error {
	e.mu.Lock()
	e.shutdownCalled = true
	e.mu.Unlock()
	return nil
}

// TestRecordEvent_DoesNotBlockOnSlowExporter verifies that RecordEvent returns
// immediately even when the exporter's Export would block indefinitely.
func TestRecordEvent_DoesNotBlockOnSlowExporter(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	// Use a time-based unique suffix so the name is distinct across -count>1 runs.
	exporterName := fmt.Sprintf("blocking-%s-%d", t.Name(), time.Now().UnixNano())
	blk := newBlockingExporter(exporterName)
	observability.RegisterExporter(exporterName, func() observability.Exporter { return blk })

	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Endpoint = ""
	cfg.Exporters = []ExporterConfig{{Name: exporterName, Enabled: true}}

	prov, shutdown, err := Init(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Init returned error: %v", err)
	}

	evt := observability.Event{Subject: "gateway.request.completed"}

	// RecordEvent must return promptly — the exporter is blocking on its block
	// channel which we have not closed yet.
	done := make(chan struct{})
	go func() {
		prov.RecordEvent(context.Background(), evt)
		close(done)
	}()

	select {
	case <-done:
		// Good: RecordEvent returned without blocking.
	case <-time.After(500 * time.Millisecond):
		t.Fatal("RecordEvent blocked for >500ms — it must not block the caller")
	}

	// Unblock the exporter so the worker can exit, then shut down cleanly.
	close(blk.block)
	shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = shutdown(shutCtx)
}

// TestShutdown_DrainsBufferedEventsBeforeReturning verifies that Shutdown
// flushes all buffered events to the exporter before calling exporter.Shutdown.
func TestShutdown_DrainsBufferedEventsBeforeReturning(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	// Use a time-based unique suffix so the name is distinct across -count>1 runs.
	exporterName := fmt.Sprintf("drain-%s-%d", t.Name(), time.Now().UnixNano())
	fake := &fakeExporter{}
	observability.RegisterExporter(exporterName, func() observability.Exporter { return fake })

	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Endpoint = ""
	cfg.Exporters = []ExporterConfig{{Name: exporterName, Enabled: true}}

	prov, shutdown, err := Init(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Init returned error: %v", err)
	}

	const n = 10
	for i := 0; i < n; i++ {
		prov.RecordEvent(context.Background(), observability.Event{Subject: "gateway.request.completed"})
	}

	// Shutdown must drain all n events before calling exporter.Shutdown.
	shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := shutdown(shutCtx); err != nil {
		t.Fatalf("shutdown returned error: %v", err)
	}

	fake.mu.Lock()
	gotN := len(fake.exported)
	shutCalled := fake.shutdownCalled
	fake.mu.Unlock()

	if gotN != n {
		t.Errorf("expected %d exported events after Shutdown drain, got %d", n, gotN)
	}
	if !shutCalled {
		t.Error("exporter.Shutdown was not called after drain")
	}
}

// TestRecordEvent_DropOnFull verifies that when the event queue is full,
// RecordEvent drops new events instead of blocking, and that at least
// eventQueueCapacity events are still successfully delivered.
func TestRecordEvent_DropOnFull(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	// Use a time-based unique suffix so the name is distinct across -count>1 runs.
	exporterName := fmt.Sprintf("dropfull-%s-%d", t.Name(), time.Now().UnixNano())
	// Use a blocking exporter to hold the queue full: Export blocks until
	// we release it, allowing us to saturate the buffer.
	blk := newBlockingExporter(exporterName)
	observability.RegisterExporter(exporterName, func() observability.Exporter { return blk })

	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Endpoint = ""
	cfg.Exporters = []ExporterConfig{{Name: exporterName, Enabled: true}}

	prov, shutdown, err := Init(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Init returned error: %v", err)
	}

	// Send more events than the queue capacity. None of these must block.
	send := eventQueueCapacity + 100
	done := make(chan struct{})
	go func() {
		for i := 0; i < send; i++ {
			prov.RecordEvent(context.Background(), observability.Event{Subject: "test"})
		}
		close(done)
	}()

	select {
	case <-done:
		// Good: all RecordEvent calls returned without blocking.
	case <-time.After(2 * time.Second):
		t.Fatal("RecordEvent calls blocked when queue was full")
	}

	// Unblock the exporter and shut down; verify at least eventQueueCapacity
	// events were delivered (exact count depends on timing).
	close(blk.block)
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = shutdown(shutCtx)

	blk.mu.Lock()
	delivered := len(blk.exported)
	blk.mu.Unlock()

	if delivered < eventQueueCapacity {
		t.Errorf("expected at least %d events delivered, got %d", eventQueueCapacity, delivered)
	}
	if delivered > send {
		t.Errorf("delivered (%d) > sent (%d): impossible", delivered, send)
	}
}

// --- resolveHeaders tests ---

func TestResolveHeaders_LiteralPassThrough(t *testing.T) {
	got := resolveHeaders(map[string]string{"x-token": "literal-value"})
	if got["x-token"] != "literal-value" {
		t.Errorf("expected literal value to pass through, got %q", got["x-token"])
	}
}

func TestResolveHeaders_EnvInterpolation(t *testing.T) {
	t.Setenv("TEST_RESOLVE_HEADER_KEY", "secret-token-123")
	got := resolveHeaders(map[string]string{"Authorization": "Bearer ${TEST_RESOLVE_HEADER_KEY}"})
	want := "Bearer secret-token-123"
	if got["Authorization"] != want {
		t.Errorf("resolveHeaders: got %q, want %q", got["Authorization"], want)
	}
}

func TestResolveHeaders_UnsetEnvVarSkipped(t *testing.T) {
	// Empty string resolves identically to an unset var for os.Expand/os.Getenv.
	t.Setenv("FERRO_OTEL_TEST_UNSET_VAR", "")
	got := resolveHeaders(map[string]string{"dd-api-key": "${FERRO_OTEL_TEST_UNSET_VAR}"})
	if _, exists := got["dd-api-key"]; exists {
		t.Errorf("expected key with empty resolved value to be omitted, got %q", got["dd-api-key"])
	}
	if got != nil {
		t.Errorf("expected nil map when all headers resolve to empty, got %v", got)
	}
}

func TestResolveHeaders_MixedMap(t *testing.T) {
	t.Setenv("FERRO_OTEL_TEST_SET_VAR", "my-api-key")
	t.Setenv("FERRO_OTEL_TEST_EMPTY_VAR", "")

	raw := map[string]string{
		"x-api-key": "${FERRO_OTEL_TEST_SET_VAR}",
		"x-dropped": "${FERRO_OTEL_TEST_EMPTY_VAR}",
		"x-literal": "static",
	}
	got := resolveHeaders(raw)

	if got["x-api-key"] != "my-api-key" {
		t.Errorf("x-api-key: got %q, want %q", got["x-api-key"], "my-api-key")
	}
	if _, exists := got["x-dropped"]; exists {
		t.Errorf("x-dropped should have been omitted, got %q", got["x-dropped"])
	}
	if got["x-literal"] != "static" {
		t.Errorf("x-literal: got %q, want %q", got["x-literal"], "static")
	}
	if len(got) != 2 {
		t.Errorf("expected 2 headers in resolved map, got %d: %v", len(got), got)
	}
}

func TestResolveHeaders_NilInput(t *testing.T) {
	got := resolveHeaders(nil)
	if got != nil {
		t.Errorf("expected nil for nil input, got %v", got)
	}
}

func TestResolveHeaders_EmptyMap(t *testing.T) {
	got := resolveHeaders(map[string]string{})
	if got != nil {
		t.Errorf("expected nil for empty input, got %v", got)
	}
}

// TestNewSpanExporter_WithHeaders verifies that newSpanExporter builds
// successfully when headers are configured (gRPC and HTTP paths).
// The OTLP SDK does not expose the configured headers for inspection;
// we assert that construction succeeds without error and that
// resolveHeaders produces the expected map (tested separately above).
func TestNewSpanExporter_WithHeaders(t *testing.T) {
	t.Setenv("FERRO_OTEL_TEST_HEADER_VAL", "test-key-abc")

	tests := []struct {
		name     string
		protocol string
	}{
		{"grpc", "grpc"},
		{"http", "http/protobuf"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Endpoint = testEndpoint
			cfg.Protocol = tc.protocol
			cfg.Headers = map[string]string{
				"x-api-key": "${FERRO_OTEL_TEST_HEADER_VAL}",
				"x-static":  "literal",
			}

			exporter, err := newSpanExporter(context.Background(), cfg)
			if err != nil {
				t.Fatalf("newSpanExporter with headers errored: %v", err)
			}
			if exporter == nil {
				t.Fatal("expected non-nil exporter")
			}
			// Shut down the exporter with a short deadline to clean up resources.
			shutCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			_ = exporter.Shutdown(shutCtx)
		})
	}
}

// TestEndpointIsSecure verifies the scheme→security decision used by
// newSpanExporter: https:// → secure, http:// → insecure,
// bare host:port → insecure (backward-compatible default).
func TestEndpointIsSecure(t *testing.T) {
	tests := []struct {
		endpoint string
		want     bool // true = secure (TLS), false = insecure
	}{
		{"https://collector.example.com:4317", true},
		{"https://localhost:4317", true},
		{"http://collector.example.com:4317", false},
		{"http://localhost:4318", false},
		{"localhost:4317", false},          // bare host:port → insecure (backward compat)
		{"collector.internal:4317", false}, // bare host:port → insecure
		{"", false},                        // empty → insecure
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.endpoint, func(t *testing.T) {
			got := endpointIsSecure(tc.endpoint)
			if got != tc.want {
				t.Errorf("endpointIsSecure(%q) = %v, want %v", tc.endpoint, got, tc.want)
			}
		})
	}
}
