package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

const providerOpenAI = "openai"

// buildTestRegistry creates a registry with an OpenAI provider pointing to upstream.
func buildTestRegistry(upstreamURL string) *providers.Registry {
	reg := providers.NewRegistry()
	p, _ := providers.NewOpenAI("sk-test-key", upstreamURL)
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
