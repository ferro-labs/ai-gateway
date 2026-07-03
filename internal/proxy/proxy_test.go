package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
	geminipkg "github.com/ferro-labs/ai-gateway/providers/gemini"
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

	p, ok := ResolveProvider(req, reg)
	if !ok {
		t.Fatal("ResolveProvider() returned false, want true")
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

	p, ok := ResolveProvider(req, reg)
	if !ok {
		t.Fatal("ResolveProvider() returned false, want true")
	}
	if p.Name() != providerOpenAI {
		t.Errorf("provider name = %q, want openai", p.Name())
	}
}

func TestResolveProvider_UnknownProvider(t *testing.T) {
	reg := buildTestRegistry("http://localhost")

	req := httptest.NewRequest(http.MethodPost, "/v1/files", nil)
	req.Header.Set("X-Provider", "nonexistent")

	_, ok := ResolveProvider(req, reg)
	if ok {
		t.Error("ResolveProvider() returned true for unknown provider, want false")
	}
}

func TestResolveProvider_NoProviderInfo(t *testing.T) {
	reg := buildTestRegistry("http://localhost")

	req := httptest.NewRequest(http.MethodPost, "/v1/files", nil)

	_, ok := ResolveProvider(req, reg)
	if ok {
		t.Error("ResolveProvider() returned true with no provider info, want false")
	}
}

func TestResolveProvider_BodyRestoredAfterRead(t *testing.T) {
	reg := buildTestRegistry("http://localhost")

	body := `{"model":"gpt-4o"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/test", strings.NewReader(body))
	req.ContentLength = int64(len(body))

	ResolveProvider(req, reg) //nolint:errcheck

	// Body should be restored and readable again.
	data, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("failed to read body after ResolveProvider: %v", err)
	}
	if string(data) != body {
		t.Errorf("body after ResolveProvider = %q, want %q", string(data), body)
	}
}

func TestResolveProvider_ModelAfterLargeNestedField(t *testing.T) {
	reg := buildTestRegistry("http://localhost")

	body := `{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"world"}],"metadata":{"nested":{"a":[1,2,3],"b":{"c":"d"}}},"model":"gpt-4o","stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(body))

	p, ok := ResolveProvider(req, reg)
	if !ok {
		t.Fatal("ResolveProvider() returned false, want true")
	}
	if p.Name() != providerOpenAI {
		t.Errorf("provider name = %q, want openai", p.Name())
	}

	data, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("failed to read body after ResolveProvider: %v", err)
	}
	if string(data) != body {
		t.Errorf("body after ResolveProvider = %q, want %q", string(data), body)
	}
}

func TestResolveProvider_IgnoresNestedModelField(t *testing.T) {
	reg := buildTestRegistry("http://localhost")

	body := `{"input":{"model":"gpt-4o"},"messages":[]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(body))

	_, ok := ResolveProvider(req, reg)
	if ok {
		t.Fatal("ResolveProvider() returned true for nested model field, want false")
	}

	data, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("failed to read body after ResolveProvider: %v", err)
	}
	if string(data) != body {
		t.Errorf("body after ResolveProvider = %q, want %q", string(data), body)
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
	handler := Handler(reg)

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
	handler := Handler(reg)

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
	handler := Handler(reg)

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
	handler := Handler(reg)

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
	handler := Handler(reg)

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
	handler := Handler(reg)

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
	handler := Handler(reg)

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", nil)
	w := httptest.NewRecorder()

	handler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}

	var body map[string]any
	_ = json.NewDecoder(w.Body).Decode(&body)
	if _, ok := body["error"]; !ok {
		t.Error("expected error field in response body")
	}
}

// TestProxyHandler_DoesNotDoubleV1Prefix guards against the pass-through proxy
// doubling the /v1 path segment for providers whose base URL already ends in
// /v1 (e.g. xai, openrouter, cerebras). The proxy is mounted at /v1/*, so the
// inbound path always carries /v1; the upstream must receive it exactly once.
func TestProxyHandler_DoesNotDoubleV1Prefix(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	// Provider whose base URL already ends in /v1.
	reg := buildTestRegistry(upstream.URL + "/v1")
	handler := Handler(reg)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
	req.Header.Set("X-Provider", providerOpenAI)
	req.ContentLength = 2
	w := httptest.NewRecorder()

	handler(w, req)

	if gotPath != "/v1/responses" {
		t.Errorf("upstream path = %q, want /v1/responses (proxy must not double the /v1 segment)", gotPath)
	}
}

// TestProxyHandler_PreservesV1PrefixForRootBase verifies that a bare-host base
// URL (no /v1 suffix) still forwards the inbound /v1 prefix intact — the fix
// must not over-trim.
func TestProxyHandler_PreservesV1PrefixForRootBase(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	reg := buildTestRegistry(upstream.URL) // base has no /v1
	handler := Handler(reg)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
	req.Header.Set("X-Provider", providerOpenAI)
	req.ContentLength = 2
	w := httptest.NewRecorder()

	handler(w, req)

	if gotPath != "/v1/responses" {
		t.Errorf("upstream path = %q, want /v1/responses", gotPath)
	}
}

// TestProxyHandler_GatesNonOpenAIWireProvider verifies a non-OpenAI-wire
// provider (Gemini) is refused with 501 rather than transparently forwarded —
// its upstream is not OpenAI-shaped, so an OpenAI-wire pass-through would fail.
func TestProxyHandler_GatesNonOpenAIWireProvider(t *testing.T) {
	g, err := geminipkg.New("test-key", "")
	if err != nil {
		t.Fatalf("gemini.New: %v", err)
	}
	reg := providers.NewRegistry()
	reg.Register(g)
	handler := Handler(reg)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
	req.Header.Set("X-Provider", g.Name())
	req.ContentLength = 2
	w := httptest.NewRecorder()

	handler(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 for non-OpenAI-wire provider", w.Code)
	}
	var body map[string]any
	_ = json.NewDecoder(w.Body).Decode(&body)
	if _, ok := body["error"]; !ok {
		t.Error("expected error envelope for gated provider")
	}
}

// stubSigningProvider is a minimal OpenAI-wire ProxiableProvider that also
// implements providers.RequestSigner, used to verify the proxy signs outbound
// requests before forwarding them.
type stubSigningProvider struct {
	baseURL string
	signed  *bool
	signErr error
}

func (stubSigningProvider) Name() string { return "stub-signer" }
func (stubSigningProvider) Complete(context.Context, providers.Request) (*providers.Response, error) {
	return nil, nil
}
func (stubSigningProvider) SupportedModels() []string      { return nil }
func (stubSigningProvider) SupportsModel(string) bool      { return true }
func (stubSigningProvider) Models() []providers.ModelInfo  { return nil }
func (s stubSigningProvider) BaseURL() string              { return s.baseURL }
func (stubSigningProvider) AuthHeaders() map[string]string { return nil }
func (s stubSigningProvider) SignProxyRequest(req *http.Request) error {
	if s.signed != nil {
		*s.signed = true
	}
	req.Header.Set("X-Signed", "1")
	return s.signErr
}

// TestProxyHandler_InvokesRequestSigner verifies that when a provider implements
// RequestSigner, the proxy signs the outbound request before forwarding it.
func TestProxyHandler_InvokesRequestSigner(t *testing.T) {
	var seenSigned string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenSigned = r.Header.Get("X-Signed")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	signed := false
	reg := providers.NewRegistry()
	reg.Register(stubSigningProvider{baseURL: upstream.URL, signed: &signed})
	handler := Handler(reg)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
	req.Header.Set("X-Provider", "stub-signer")
	req.ContentLength = 2
	w := httptest.NewRecorder()

	handler(w, req)

	if !signed {
		t.Fatal("RequestSigner.SignProxyRequest was not called")
	}
	if seenSigned != "1" {
		t.Errorf("upstream X-Signed = %q, want 1 (signer header not applied)", seenSigned)
	}
}

// TestProxyHandler_RequestSignerFailure_Returns502 verifies that when signing
// fails the proxy surfaces a 502 (via ErrorHandler) and never reaches upstream,
// rather than forwarding an unsigned request.
func TestProxyHandler_RequestSignerFailure_Returns502(t *testing.T) {
	var reached bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	reg := providers.NewRegistry()
	reg.Register(stubSigningProvider{baseURL: upstream.URL, signErr: errors.New("sign failed")})
	handler := Handler(reg)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
	req.Header.Set("X-Provider", "stub-signer")
	req.ContentLength = 2
	w := httptest.NewRecorder()

	handler(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 when signing fails", w.Code)
	}
	if reached {
		t.Error("upstream must not be reached when signing fails")
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
		model, err := ExtractTopLevelModel(req)
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
		p, ok := ResolveProvider(req, reg)
		if !ok {
			b.Fatal("ResolveProvider() returned false")
		}
		if p.Name() != providerOpenAI {
			b.Fatalf("provider = %q, want %q", p.Name(), providerOpenAI)
		}
	}
}

// TestProxyHandler_ErrorHandler_GenericJSON verifies that when the upstream is
// unreachable the proxy returns a JSON error envelope consistent with the
// project's apierror shape rather than leaking a raw Go error string via
// http.Error (which would set Content-Type: text/plain and expose internals).
func TestProxyHandler_ErrorHandler_GenericJSON(t *testing.T) {
	// Create a server, record its URL, then close it so connections are refused.
	dead := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	reg := buildTestRegistry(deadURL)
	handler := Handler(reg)

	req := httptest.NewRequest(http.MethodPost, "/v1/files", nil)
	req.Header.Set("X-Provider", providerOpenAI)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}

	// Content-Type must be application/json, not text/plain from http.Error.
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	// Body must decode as a JSON error envelope.
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("response body is not valid JSON: %v\nbody: %s", err, w.Body.String())
	}
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("response body has no 'error' object: %v", resp)
	}

	// The message must NOT expose a raw Go error string ("proxy error: ...").
	msg, _ := errObj["message"].(string)
	if strings.Contains(msg, "proxy error:") {
		t.Errorf("response message leaks internal error: %q", msg)
	}
	if msg == "" {
		t.Error("response 'error.message' is empty")
	}
}
