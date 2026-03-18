package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
	openaipkg "github.com/ferro-labs/ai-gateway/providers/openai"
)

const providerOpenAI = "openai"

// buildTestRegistry creates a registry with an OpenAI provider pointing to upstream.
func buildTestRegistry(upstreamURL string) *providers.Registry {
	reg := providers.NewRegistry()
	p, _ := openaipkg.New("sk-test-key", upstreamURL)
	reg.Register(p)
	return reg
}

func TestResolveProvider_XProviderHeader(t *testing.T) {
	reg := buildTestRegistry("http://localhost")

	req := httptest.NewRequest(http.MethodPost, "/v1/files", nil)
	req.Header.Set("X-Provider", providerOpenAI)

	p, ok := resolveProvider(req, reg)
	if !ok {
		t.Fatal("resolveProvider() returned false, want true")
	}
	if p.Name() != providerOpenAI {
		t.Errorf("provider name = %q, want openai", p.Name())
	}
}

func TestResolveProvider_ModelInBody(t *testing.T) {
	reg := buildTestRegistry("http://localhost")

	body := `{"model":"gpt-4o","messages":[]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(body))

	p, ok := resolveProvider(req, reg)
	if !ok {
		t.Fatal("resolveProvider() returned false, want true")
	}
	if p.Name() != providerOpenAI {
		t.Errorf("provider name = %q, want openai", p.Name())
	}
}

func TestResolveProvider_UnknownProvider(t *testing.T) {
	reg := buildTestRegistry("http://localhost")

	req := httptest.NewRequest(http.MethodPost, "/v1/files", nil)
	req.Header.Set("X-Provider", "nonexistent")

	_, ok := resolveProvider(req, reg)
	if ok {
		t.Error("resolveProvider() returned true for unknown provider, want false")
	}
}

func TestResolveProvider_NoProviderInfo(t *testing.T) {
	reg := buildTestRegistry("http://localhost")

	req := httptest.NewRequest(http.MethodPost, "/v1/files", nil)

	_, ok := resolveProvider(req, reg)
	if ok {
		t.Error("resolveProvider() returned true with no provider info, want false")
	}
}

func TestResolveProvider_BodyRestoredAfterRead(t *testing.T) {
	reg := buildTestRegistry("http://localhost")

	body := `{"model":"gpt-4o"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/test", strings.NewReader(body))
	req.ContentLength = int64(len(body))

	resolveProvider(req, reg) //nolint:errcheck

	// Body should be restored and readable again.
	data, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("failed to read body after resolveProvider: %v", err)
	}
	if string(data) != body {
		t.Errorf("body after resolveProvider = %q, want %q", string(data), body)
	}
}

func TestResolveProvider_ModelAfterLargeNestedField(t *testing.T) {
	reg := buildTestRegistry("http://localhost")

	body := `{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"world"}],"metadata":{"nested":{"a":[1,2,3],"b":{"c":"d"}}},"model":"gpt-4o","stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(body))

	p, ok := resolveProvider(req, reg)
	if !ok {
		t.Fatal("resolveProvider() returned false, want true")
	}
	if p.Name() != providerOpenAI {
		t.Errorf("provider name = %q, want openai", p.Name())
	}

	data, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("failed to read body after resolveProvider: %v", err)
	}
	if string(data) != body {
		t.Errorf("body after resolveProvider = %q, want %q", string(data), body)
	}
}

func TestResolveProvider_IgnoresNestedModelField(t *testing.T) {
	reg := buildTestRegistry("http://localhost")

	body := `{"input":{"model":"gpt-4o"},"messages":[]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(body))

	_, ok := resolveProvider(req, reg)
	if ok {
		t.Fatal("resolveProvider() returned true for nested model field, want false")
	}

	data, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("failed to read body after resolveProvider: %v", err)
	}
	if string(data) != body {
		t.Errorf("body after resolveProvider = %q, want %q", string(data), body)
	}
}

func TestProxyHandler_ForwardsRequest(t *testing.T) {
	// Upstream server that echoes back a 200.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":"ok"}`))
	}))
	defer upstream.Close()

	reg := buildTestRegistry(upstream.URL)
	handler := proxyHandler(reg)

	req := httptest.NewRequest(http.MethodPost, "/v1/files", strings.NewReader(`{}`))
	req.Header.Set("X-Provider", providerOpenAI)
	req.ContentLength = 2
	w := httptest.NewRecorder()

	handler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("proxy status = %d, want 200", w.Code)
	}
}

func TestProxyHandler_InjectsAuthHeader(t *testing.T) {
	// Upstream server that inspects the Authorization header.
	var receivedAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	reg := buildTestRegistry(upstream.URL)
	handler := proxyHandler(reg)

	req := httptest.NewRequest(http.MethodPost, "/v1/files", nil)
	req.Header.Set("X-Provider", providerOpenAI)
	w := httptest.NewRecorder()

	handler(w, req)

	if !strings.HasPrefix(receivedAuth, "Bearer ") {
		t.Errorf("upstream received Authorization = %q, want Bearer ...", receivedAuth)
	}
}

func TestProxyHandler_RemovesGatewayHeaders(t *testing.T) {
	var seenXProvider string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenXProvider = r.Header.Get("X-Provider")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	reg := buildTestRegistry(upstream.URL)
	handler := proxyHandler(reg)

	req := httptest.NewRequest(http.MethodPost, "/v1/files", nil)
	req.Header.Set("X-Provider", providerOpenAI)
	w := httptest.NewRecorder()

	handler(w, req)

	if seenXProvider != "" {
		t.Errorf("X-Provider header leaked to upstream: %q", seenXProvider)
	}
}

func TestProxyHandler_AddsGatewayProviderHeader(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	reg := buildTestRegistry(upstream.URL)
	handler := proxyHandler(reg)

	req := httptest.NewRequest(http.MethodPost, "/v1/files", nil)
	req.Header.Set("X-Provider", providerOpenAI)
	w := httptest.NewRecorder()

	handler(w, req)

	if w.Header().Get("X-Gateway-Provider") != providerOpenAI {
		t.Errorf("X-Gateway-Provider = %q, want openai", w.Header().Get("X-Gateway-Provider"))
	}
}

func TestProxyHandler_RebuildsForwardedHeaders(t *testing.T) {
	var receivedXFF string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedXFF = r.Header.Get("X-Forwarded-For")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	reg := buildTestRegistry(upstream.URL)
	handler := proxyHandler(reg)

	req := httptest.NewRequest(http.MethodPost, "/v1/files", nil)
	req.RemoteAddr = "203.0.113.10:1234"
	req.Header.Set("X-Provider", providerOpenAI)
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	w := httptest.NewRecorder()

	handler(w, req)

	if strings.Contains(receivedXFF, "1.2.3.4") {
		t.Fatalf("spoofed X-Forwarded-For leaked upstream: %q", receivedXFF)
	}
	if !strings.Contains(receivedXFF, "203.0.113.10") {
		t.Fatalf("rebuilt X-Forwarded-For = %q, want client IP", receivedXFF)
	}
}

func TestProxyHandler_PassthroughNon200(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer upstream.Close()

	reg := buildTestRegistry(upstream.URL)
	handler := proxyHandler(reg)

	req := httptest.NewRequest(http.MethodPost, "/v1/files", nil)
	req.Header.Set("X-Provider", providerOpenAI)
	w := httptest.NewRecorder()

	handler(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("proxy status = %d, want 429", w.Code)
	}
}

func TestProxyHandler_NoProvider_Returns400(t *testing.T) {
	reg := providers.NewRegistry() // empty registry
	handler := proxyHandler(reg)

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", nil)
	w := httptest.NewRecorder()

	handler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}

	var body map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&body)
	if _, ok := body["error"]; !ok {
		t.Error("expected error field in response body")
	}
}

func BenchmarkExtractTopLevelModel(b *testing.B) {
	body := []byte(`{
		"messages":[
			{"role":"system","content":"You are a routing benchmark."},
			{"role":"user","content":"Find the best provider for this request."}
		],
		"metadata":{
			"tenant":"bench",
			"tags":["proxy","model-scan"],
			"nested":{"a":[1,2,3],"b":{"c":"d","e":["x","y","z"]}}
		},
		"tools":[
			{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}
		],
		"model":"gpt-4o",
		"stream":true
	}`)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := &http.Request{
			Method:        http.MethodPost,
			URL:           &url.URL{Path: "/v1/chat/completions"},
			Header:        make(http.Header),
			Body:          io.NopCloser(bytes.NewReader(body)),
			ContentLength: int64(len(body)),
		}
		model, err := extractTopLevelModel(req)
		if err != nil {
			b.Fatal(err)
		}
		if model != "gpt-4o" {
			b.Fatalf("model = %q, want gpt-4o", model)
		}
	}
}

func BenchmarkResolveProvider_ModelInBody(b *testing.B) {
	reg := buildTestRegistry("http://localhost")
	body := []byte(`{
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"world"}
		],
		"metadata":{"tenant":"bench","trace_id":"trace-123"},
		"model":"gpt-4o",
		"stream":false
	}`)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := &http.Request{
			Method:        http.MethodPost,
			URL:           &url.URL{Path: "/v1/files"},
			Header:        make(http.Header),
			Body:          io.NopCloser(bytes.NewReader(body)),
			ContentLength: int64(len(body)),
		}
		p, ok := resolveProvider(req, reg)
		if !ok {
			b.Fatal("resolveProvider() returned false")
		}
		if p.Name() != providerOpenAI {
			b.Fatalf("provider = %q, want %q", p.Name(), providerOpenAI)
		}
	}
}
