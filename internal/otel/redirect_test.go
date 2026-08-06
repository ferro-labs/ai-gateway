package otel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/observability"
)

// TestOTLPExporterDoesNotFollowRedirects pins SEC-007.
//
// The OTLP/HTTP exporter is built by the OpenTelemetry SDK, which constructs
// its own http.Client with no CheckRedirect and therefore follows up to ten
// hops. It carries observability.tracing.headers — values the config
// documents as holding ${ENV_VAR} secrets and that CLAUDE.md classifies as
// transport credentials — so a redirecting or misconfigured collector would
// have them replayed to a host the operator never configured. Go strips
// Authorization on a cross-domain hop but copies custom headers verbatim, and
// the real-world values here (x-honeycomb-team, api-key) are custom headers.
//
// This is the harm frame SEC-002 established, on a client built by a
// dependency rather than by this repository.
func TestOTLPExporterDoesNotFollowRedirects(t *testing.T) {
	var targetHits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		if got := r.Header.Get("X-Sentinel-Token"); got != "" {
			t.Errorf("credential header replayed to the redirect target")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	var firstHits atomic.Int64
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHits.Add(1)
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound)
	}))
	defer first.Close()

	cfg := Config{
		Enabled:       true,
		Endpoint:      strings.TrimPrefix(first.URL, "http://"),
		Protocol:      "http/protobuf",
		ServiceName:   "ferrogw-sec007-test",
		SampleRatio:   1.0,
		PrivacyLevel:  PrivacyLevelMetadata,
		ShutdownGrace: 5 * time.Second,
		Headers:       map[string]string{"X-Sentinel-Token": "sentinel-not-a-real-credential-0006"},
	}

	initCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	prov, shutdown, err := Init(initCtx, cfg)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Produce one span, then shut down to force the batch processor to export.
	_, span := prov.StartRequestSpan(context.Background(), observability.RequestAttrs{
		System:       "stub",
		Operation:    "chat",
		RequestModel: "stub-model",
	})
	span.End()

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	_ = shutdown(shutdownCtx)

	if firstHits.Load() == 0 {
		t.Fatal("the collector endpoint was never contacted; the test proved nothing")
	}
	if n := targetHits.Load(); n != 0 {
		t.Errorf("the redirect target was contacted %d time(s); the OTLP exporter followed the redirect and replayed the exporter headers", n)
	}
}

// TestOTLPExportTimeoutIsPositive guards the constant the test above depends on.
//
// httpclient.New(0) returns the shared default client, whose transport is
// otelhttp-wrapped. Handing that to the exporter makes it trace its own
// exports, and each export's spans generate further exports. A zero or negative
// otlpExportTimeout is the only way to reach it from here.
func TestOTLPExportTimeoutIsPositive(t *testing.T) {
	if otlpExportTimeout <= 0 {
		t.Fatalf("otlpExportTimeout = %v; must be positive or httpclient.New returns the otelhttp-instrumented shared client", otlpExportTimeout)
	}
	if httpclient.New(otlpExportTimeout) == httpclient.New(0) {
		t.Fatal("the OTLP exporter client is the shared instrumented default; exports would be traced by the exporter that sends them")
	}
}
