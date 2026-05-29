//go:build integration
// +build integration

package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	gwotel "github.com/ferro-labs/ai-gateway/internal/otel"
)

// otlpReceiver is a minimal stub OTLP/HTTP receiver that captures every
// POST to /v1/traces. It exists so the gateway's full observability
// pipeline (Provider → SDK → batch exporter → OTLP/HTTP) can be
// exercised end-to-end without standing up a real collector.
type otlpReceiver struct {
	mu      sync.Mutex
	posts   int
	lastCT  string
	lastLen int
}

func (r *otlpReceiver) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/v1/traces" {
			http.NotFound(w, req)
			return
		}
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.posts++
		r.lastCT = req.Header.Get("Content-Type")
		r.lastLen = len(body)
		r.mu.Unlock()

		// OTLP/HTTP success response is an empty protobuf-or-JSON
		// ExportTraceServiceResponse. An empty body with 200 is
		// accepted by the SDK's HTTP exporter.
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	})
}

func (r *otlpReceiver) snapshot() (posts int, contentType string, lastLen int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.posts, r.lastCT, r.lastLen
}

// TestEndToEnd_OTLPSpanReachesCollector wires the real OTel pipeline
// (internal/otel.Init) into a wired test gateway, performs a chat
// completion through the HTTP router, and asserts that at least one
// OTLP/HTTP export request reaches the stub receiver.
//
// This is the acceptance test for Issue #49 — it proves the full chain
// of: HTTP middleware → gateway root span → SDK batcher → OTLP HTTP
// exporter → collector is wired correctly.
func TestEndToEnd_OTLPSpanReachesCollector(t *testing.T) {
	// 1. Stub OTLP receiver.
	rec := &otlpReceiver{}
	collector := httptest.NewServer(rec.handler())
	defer collector.Close()

	// 2. Build a real OTel provider pointed at the stub.
	endpoint := strings.TrimPrefix(collector.URL, "http://")
	cfg := gwotel.Config{
		Enabled:       true,
		Endpoint:      endpoint,
		Protocol:      "http/protobuf",
		ServiceName:   "ferrogw-integration-test",
		SampleRatio:   1.0,
		PrivacyLevel:  gwotel.PrivacyLevelMetadata,
		ShutdownGrace: 5 * time.Second,
	}
	initCtx, cancelInit := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelInit()
	prov, shutdown, err := gwotel.Init(initCtx, cfg)
	if err != nil {
		t.Fatalf("otel.Init: %v", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdown(shutdownCtx); err != nil {
			t.Logf("otel shutdown: %v", err)
		}
	}()

	// 3. Wire a normal test gateway with the real provider installed.
	env := newTestServer(t)
	env.Gateway.SetObservability(prov)

	// 4. Issue a chat completion through the wired router.
	body := map[string]any{
		"model":    stubModelName,
		"messages": []map[string]string{{"role": "user", "content": "hello otel"}},
	}
	payload, _ := json.Marshal(body)
	req, _ := http.NewRequest(
		http.MethodPost,
		env.Server.URL+"/v1/chat/completions",
		bytes.NewReader(payload),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("chat completions: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(respBody))
	}
	_ = resp.Body.Close()

	// 5. Force the batch span processor to flush by shutting down
	//    the provider. This is the supported way to deterministically
	//    drain spans in a test; production uses BatchTimeout.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	// 6. Assert the collector saw an OTLP export request.
	posts, ct, n := rec.snapshot()
	if posts == 0 {
		t.Fatalf("expected at least one OTLP /v1/traces POST, got 0")
	}
	if !strings.Contains(ct, "x-protobuf") {
		t.Fatalf("expected OTLP/HTTP content-type with x-protobuf, got %q", ct)
	}
	if n == 0 {
		t.Fatalf("expected non-empty OTLP payload, got 0 bytes")
	}
}
