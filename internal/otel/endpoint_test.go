package otel

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"net/http"

	"github.com/ferro-labs/ai-gateway/observability"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// collectorStub is an OTLP/HTTP collector that records the paths it was posted
// to. The body is not decoded — this file is about *where* the exporter sends,
// which is precisely what the endpoint handling used to get wrong.
type collectorStub struct {
	*httptest.Server
	paths chan string
}

func newCollectorStub(t *testing.T) *collectorStub {
	t.Helper()
	c := &collectorStub{paths: make(chan string, 16)}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case c.paths <- r.URL.Path:
		default:
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(c.Close)
	return c
}

// awaitPath waits for one export to arrive, or fails.
func (c *collectorStub) awaitPath(t *testing.T) string {
	t.Helper()
	select {
	case p := <-c.paths:
		return p
	case <-time.After(10 * time.Second):
		t.Fatal("no OTLP export reached the collector within 10s")
		return ""
	}
}

// exportOneSpan builds the exporter cfg describes, pushes a single span through
// a real TracerProvider, and flushes.
func exportOneSpan(t *testing.T, cfg Config) {
	t.Helper()
	exporter, err := newSpanExporter(context.Background(), cfg)
	if err != nil {
		t.Fatalf("newSpanExporter: %v", err)
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter))
	_, span := tp.Tracer("endpoint_test").Start(context.Background(), "probe")
	span.End()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := tp.ForceFlush(ctx); err != nil {
		t.Fatalf("force flush: %v", err)
	}
	_ = tp.Shutdown(ctx)
}

// TestHTTPEndpointWithPathExportsSpans is the regression test for a collector
// URL carrying a path exporting nothing at all.
//
// The endpoint used to be reduced to "host:port" by stripping only the scheme,
// so the remaining path travelled into WithEndpoint, which percent-encoded it
// into the hostname. Every export then failed to parse its own URL and no span
// ever left the process, while the gateway reported tracing as enabled.
func TestHTTPEndpointWithPathExportsSpans(t *testing.T) {
	c := newCollectorStub(t)

	// The form a collector's own documentation prints.
	exportOneSpan(t, Config{
		Enabled:  true,
		Protocol: "http/protobuf",
		Endpoint: c.URL + "/otlp",
	})

	if got, want := c.awaitPath(t), "/otlp/v1/traces"; got != want {
		t.Errorf("exported to %q, want %q", got, want)
	}
}

// TestHTTPEndpointWithoutPathAppendsSignalPath pins the base-endpoint rule from
// the OTLP exporter specification: a non-signal-specific endpoint is a base URL
// and traces are sent to v1/traces relative to it.
func TestHTTPEndpointWithoutPathAppendsSignalPath(t *testing.T) {
	c := newCollectorStub(t)

	exportOneSpan(t, Config{Enabled: true, Protocol: "http/protobuf", Endpoint: c.URL})

	if got, want := c.awaitPath(t), "/v1/traces"; got != want {
		t.Errorf("exported to %q, want %q", got, want)
	}
}

// TestHTTPEndpointWithSignalPathIsNotDoubled covers the other half of the
// pasted-URL case: the URL a collector's documentation prints is usually the
// full signal URL, and appending v1/traces to it produced
// /v1/traces/v1/traces — a 404 per export batch and not one span stored.
func TestHTTPEndpointWithSignalPathIsNotDoubled(t *testing.T) {
	for _, endpointSuffix := range []string{"/v1/traces", "/v1/traces/", "/otlp/v1/traces"} {
		t.Run(endpointSuffix, func(t *testing.T) {
			c := newCollectorStub(t)

			exportOneSpan(t, Config{Enabled: true, Protocol: "http/protobuf", Endpoint: c.URL + endpointSuffix})

			want := strings.TrimSuffix(endpointSuffix, "/")
			if got := c.awaitPath(t); got != want {
				t.Errorf("exported to %q, want %q", got, want)
			}
		})
	}
}

// TestBareHostPortStillExports keeps the pre-existing host:port form working —
// it is what every current deployment has configured.
func TestBareHostPortStillExports(t *testing.T) {
	c := newCollectorStub(t)

	host := strings.TrimPrefix(c.URL, "http://")
	exportOneSpan(t, Config{Enabled: true, Protocol: "http/protobuf", Endpoint: host})

	if got, want := c.awaitPath(t), "/v1/traces"; got != want {
		t.Errorf("exported to %q, want %q", got, want)
	}
}

// TestEnvEndpointIsLeftToTheSDK proves the gateway stops interpreting the
// standard variables itself.
//
// OTEL_EXPORTER_OTLP_TRACES_ENDPOINT is specified to be used verbatim, with no
// signal path appended — the opposite of the non-signal-specific variable. The
// gateway cannot honour both rules by parsing one string, so it passes no
// endpoint option and lets the SDK apply them.
func TestEnvEndpointIsLeftToTheSDK(t *testing.T) {
	c := newCollectorStub(t)
	t.Setenv(envTracesEndpoint, c.URL+"/collect/traces")

	exportOneSpan(t, Config{Enabled: true, Protocol: "http/protobuf"})

	if got, want := c.awaitPath(t), "/collect/traces"; got != want {
		t.Errorf("exported to %q, want %q — the signal-specific variable must be used verbatim", got, want)
	}
}

// TestSignalEndpointAloneEnablesTracing covers the companion gap: setting only
// the signal-specific variable is a complete traces configuration, and used to
// leave the gateway on a NoOp provider with tracing silently off.
func TestSignalEndpointAloneEnablesTracing(t *testing.T) {
	c := newCollectorStub(t)
	t.Setenv(envTracesEndpoint, c.URL+"/v1/traces")

	prov, shutdown, err := Init(context.Background(), Config{
		Enabled:     true,
		Protocol:    "http/protobuf",
		SampleRatio: 1.0,
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if prov == nil {
		t.Fatal("Init returned a nil provider")
	}
	_, span := prov.StartRequestSpan(context.Background(), observability.RequestAttrs{System: "probe", Operation: "chat"})
	span.End()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		t.Fatalf("shutdown/flush: %v", err)
	}

	if got, want := c.awaitPath(t), "/v1/traces"; got != want {
		t.Errorf("exported to %q, want %q", got, want)
	}
}

// TestEnvEndpointOverridesConfiguredEndpoint keeps the documented precedence:
// the environment wins over observability.tracing.endpoint.
func TestEnvEndpointOverridesConfiguredEndpoint(t *testing.T) {
	c := newCollectorStub(t)
	t.Setenv(envEndpoint, c.URL)

	exportOneSpan(t, Config{
		Enabled:  true,
		Protocol: "http/protobuf",
		Endpoint: "http://127.0.0.1:1/never-used",
	})

	if got, want := c.awaitPath(t), "/v1/traces"; got != want {
		t.Errorf("exported to %q, want %q", got, want)
	}
}

// TestInvalidEndpointIsRejectedAtValidation covers the other half of the
// finding: an endpoint the exporter cannot use must fail loudly at startup
// rather than disable tracing quietly.
func TestInvalidEndpointIsRejectedAtValidation(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
	}{
		{"unsupported scheme", "grpc://collector:4317"},
		{"scheme with no host", "http://"},
		{"path without a scheme", "127.0.0.1:4318/v1/traces"},
		{"unparseable", "http://coll ector:4318"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Enabled: true, Protocol: "http/protobuf", Endpoint: tc.endpoint}
			if err := cfg.Validate(); err == nil {
				t.Fatalf("Validate accepted endpoint %q", tc.endpoint)
			}
			if _, _, err := Init(context.Background(), cfg); err == nil {
				t.Fatalf("Init accepted endpoint %q", tc.endpoint)
			}
		})
	}
}

// TestResolvedHTTPEndpointHasACleanHost is the direct assertion on the observed
// corruption: the host must never end up carrying a percent-encoded path.
func TestResolvedHTTPEndpointHasACleanHost(t *testing.T) {
	got, err := parseConfiguredEndpoint("http://127.0.0.1:4318/v1/traces", true)
	if err != nil {
		t.Fatalf("parseConfiguredEndpoint: %v", err)
	}
	u, err := url.Parse(got.url)
	if err != nil {
		t.Fatalf("resolved endpoint %q does not parse: %v", got.url, err)
	}
	if u.Host != "127.0.0.1:4318" {
		t.Errorf("host = %q, want %q", u.Host, "127.0.0.1:4318")
	}
	if strings.Contains(u.Host, "%") {
		t.Errorf("host %q carries a percent-encoded path", u.Host)
	}
}
