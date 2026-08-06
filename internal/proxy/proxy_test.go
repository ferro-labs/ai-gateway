package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/streamio"
	"github.com/ferro-labs/ai-gateway/providers"
	azurefoundrypkg "github.com/ferro-labs/ai-gateway/providers/azure_foundry"
	coherepkg "github.com/ferro-labs/ai-gateway/providers/cohere"
	geminipkg "github.com/ferro-labs/ai-gateway/providers/gemini"
	ollamapkg "github.com/ferro-labs/ai-gateway/providers/ollama"
	openaipkg "github.com/ferro-labs/ai-gateway/providers/openai"
	perplexitypkg "github.com/ferro-labs/ai-gateway/providers/perplexity"
)

const providerOpenAI = "openai"

// buildTestRegistry creates a registry with an OpenAI provider pointing to upstream.
func buildTestRegistry(tb testing.TB, upstreamURL string) *providers.Registry {
	tb.Helper()
	reg := providers.NewRegistry()
	p, err := openaipkg.New("sk-test-key", upstreamURL)
	if err != nil {
		tb.Fatalf("openai.New: %v", err)
	}
	reg.Register(p)
	return reg
}

func TestResolveProvider_XProviderHeader(t *testing.T) {
	reg := buildTestRegistry(t, "http://localhost")

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/files", nil)
	req.Header.Set("X-Provider", providerOpenAI)

	p, _, ok := ResolveProvider(req, reg)
	if !ok {
		t.Fatal("ResolveProvider() returned false, want true")
	}
	if p.Name() != providerOpenAI {
		t.Errorf("provider name = %q, want openai", p.Name())
	}
}

func TestResolveProvider_ModelInBody(t *testing.T) {
	reg := buildTestRegistry(t, "http://localhost")

	body := `{"model":"gpt-4o","messages":[]}`
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(body))

	p, _, ok := ResolveProvider(req, reg)
	if !ok {
		t.Fatal("ResolveProvider() returned false, want true")
	}
	if p.Name() != providerOpenAI {
		t.Errorf("provider name = %q, want openai", p.Name())
	}
}

func TestResolveProvider_UnknownProvider(t *testing.T) {
	reg := buildTestRegistry(t, "http://localhost")

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/files", nil)
	req.Header.Set("X-Provider", "nonexistent")

	_, _, ok := ResolveProvider(req, reg)
	if ok {
		t.Error("ResolveProvider() returned true for unknown provider, want false")
	}
}

func TestResolveProvider_NoProviderInfo(t *testing.T) {
	reg := buildTestRegistry(t, "http://localhost")

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/files", nil)

	_, _, ok := ResolveProvider(req, reg)
	if ok {
		t.Error("ResolveProvider() returned true with no provider info, want false")
	}
}

func TestResolveProvider_BodyRestoredAfterRead(t *testing.T) {
	reg := buildTestRegistry(t, "http://localhost")

	body := `{"model":"gpt-4o"}`
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/test", strings.NewReader(body))
	req.ContentLength = int64(len(body))

	ResolveProvider(req, reg)

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
	reg := buildTestRegistry(t, "http://localhost")

	body := `{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"world"}],"metadata":{"nested":{"a":[1,2,3],"b":{"c":"d"}}},"model":"gpt-4o","stream":true}`
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(body))

	p, _, ok := ResolveProvider(req, reg)
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
	reg := buildTestRegistry(t, "http://localhost")

	body := `{"input":{"model":"gpt-4o"},"messages":[]}`
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(body))

	_, _, ok := ResolveProvider(req, reg)
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

	reg := buildTestRegistry(t, upstream.URL)
	handler := Handler(reg)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/files", strings.NewReader(`{}`))
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

	reg := buildTestRegistry(t, upstream.URL)
	handler := Handler(reg)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/files", nil)
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

	reg := buildTestRegistry(t, upstream.URL)
	handler := Handler(reg)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/files", nil)
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

	reg := buildTestRegistry(t, upstream.URL)
	handler := Handler(reg)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/files", nil)
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

	reg := buildTestRegistry(t, upstream.URL)
	handler := Handler(reg)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/files", nil)
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

	reg := buildTestRegistry(t, upstream.URL)
	handler := Handler(reg)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/files", nil)
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

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/audio/transcriptions", nil)
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
	reg := buildTestRegistry(t, upstream.URL+"/v1")
	handler := Handler(reg)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
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

	reg := buildTestRegistry(t, upstream.URL) // base has no /v1
	handler := Handler(reg)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
	req.Header.Set("X-Provider", providerOpenAI)
	req.ContentLength = 2
	w := httptest.NewRecorder()

	handler(w, req)

	if gotPath != "/v1/responses" {
		t.Errorf("upstream path = %q, want /v1/responses", gotPath)
	}
}

// rootShapeProvider is a ProxiableProvider whose BaseURL is exactly the string
// the test hands it, so the table below reads one root shape per row rather than
// whatever a real provider's constructor normalises its input into.
type rootShapeProvider struct {
	name    string
	baseURL string
}

func (p rootShapeProvider) Name() string            { return p.name }
func (rootShapeProvider) SupportsModel(string) bool { return true }
func (rootShapeProvider) Complete(context.Context, providers.Request) (*providers.Response, error) {
	return nil, nil
}
func (p rootShapeProvider) BaseURL() string              { return p.baseURL }
func (rootShapeProvider) AuthHeaders() map[string]string { return nil }

// TestPassThroughPathAssemblyAcrossRootShapes pins where the pass-through puts
// an operation for every base-URL shape in the tree.
//
// The rule under test: `<PROVIDER>_BASE_URL` is the API ROOT, used verbatim, so
// the operation hangs directly beneath it. The gateway is mounted at /v1/*, so
// the /v1 to remove is the gateway's own, on the INBOUND path — not a suffix
// guessed off the end of the root. Reading it off the root only worked for a
// root whose version segment came last, and inserted /v1 mid-path for every
// other shape.
//
// A root carrying no path is no exception. It is still an API root, so the
// operation still hangs directly beneath it — perplexity's really is the bare
// host, and its own chat surface builds /chat/completions from exactly that.
// A provider configured with a SERVER root instead resolves the difference in
// its constructor and reports the API root, which is what BaseURL asks for;
// the two cases are indistinguishable from the URL string alone, so nothing
// here tries to tell them apart.
func TestPassThroughPathAssemblyAcrossRootShapes(t *testing.T) {
	tests := []struct {
		name string
		root string
		want string
	}{
		{name: "trailing-v1", root: "/v1", want: "/v1/responses"},
		{name: "mounted-api-root", root: "/custom/api", want: "/custom/api/responses"},
		{name: "deepinfra-shape", root: "/v1/openai", want: "/v1/openai/responses"},
		{name: "databricks-shape", root: "/serving-endpoints", want: "/serving-endpoints/responses"},
		{name: "groq-shape", root: "/openai/v1", want: "/openai/v1/responses"},
		{name: "perplexity-host-shape", root: "", want: "/responses"},
		{name: "host-root-trailing-slash", root: "/", want: "/responses"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observed := make(chan string, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				observed <- r.URL.Path
				w.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(upstream.Close)

			const name = "root-shape-probe"
			reg := providers.NewRegistry()
			reg.Register(rootShapeProvider{name: name, baseURL: upstream.URL + tt.root})

			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
			req.Header.Set("X-Provider", name)
			req.ContentLength = 2
			rec := httptest.NewRecorder()
			Handler(reg)(rec, req)

			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
			}
			if got := <-observed; got != tt.want {
				t.Errorf("root %q: upstream path = %q, want %q", tt.root, got, tt.want)
			}
		})
	}
}

// TestPassThroughHonoursHostRootProviders pins what the three documented
// host-root providers — the ones whose configured base is deliberately NOT an
// API root, listed in providers.hostRootProviders() — get from the rule above,
// built through their own constructors rather than through a stub.
//
// Two of them never reach path assembly: cohere and azure-foundry are
// NonOpenAIWireProvider and are refused 501 first. Ollama is the one that does,
// and it is the case the rule above delegates to the provider: OLLAMA_HOST is a
// server root, so ollama resolves the OpenAI-compatible surface in New and
// reports that as its API root.
//
// Perplexity is here as the other half of the same rule. Its base is host-shaped
// too, and reads identically, but it really is an API root — so it must get the
// opposite answer from the same code. Preserving the gateway's /v1 for any
// pathless root sent its traffic to /v1/responses on a host that mounts
// /responses, which is what the two built-through-their-constructors cases below
// exist to keep apart.
func TestPassThroughHonoursHostRootProviders(t *testing.T) {
	t.Run("perplexity-forwards-at-the-host", func(t *testing.T) {
		observed := make(chan string, 1)
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			observed <- r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		}))
		t.Cleanup(upstream.Close)

		// Perplexity's operations hang at the host: p.baseURL + "/chat/completions".
		p, err := perplexitypkg.New("test-key", upstream.URL)
		if err != nil {
			t.Fatalf("perplexity.New: %v", err)
		}
		reg := providers.NewRegistry()
		reg.Register(p)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
		req.Header.Set("X-Provider", "perplexity")
		req.ContentLength = 2
		rec := httptest.NewRecorder()
		Handler(reg)(rec, req)

		if got := <-observed; got != "/responses" {
			t.Errorf("upstream path = %q, want /responses (Perplexity mounts its operations at the host)", got)
		}
	})

	t.Run("ollama-forwards-under-the-v1-surface", func(t *testing.T) {
		observed := make(chan string, 1)
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			observed <- r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		}))
		t.Cleanup(upstream.Close)

		// OLLAMA_HOST is the server root; one server mounts /v1 and /api.
		p, err := ollamapkg.New(upstream.URL, nil)
		if err != nil {
			t.Fatalf("ollama.New: %v", err)
		}
		reg := providers.NewRegistry()
		reg.Register(p)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
		req.Header.Set("X-Provider", "ollama")
		req.ContentLength = 2
		rec := httptest.NewRecorder()
		Handler(reg)(rec, req)

		if got := <-observed; got != "/v1/responses" {
			t.Errorf("upstream path = %q, want /v1/responses (Ollama's OpenAI-compatible surface)", got)
		}
	})

	for _, tc := range []struct {
		name string
		p    providers.Provider
	}{
		{name: "cohere", p: mustCohere(t)},
		{name: "azure-foundry", p: mustAzureFoundry(t)},
	} {
		t.Run(tc.name+"-never-reaches-path-assembly", func(t *testing.T) {
			reg := providers.NewRegistry()
			reg.Register(tc.p)

			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
			req.Header.Set("X-Provider", tc.name)
			req.ContentLength = 2
			rec := httptest.NewRecorder()
			Handler(reg)(rec, req)

			if rec.Code != http.StatusNotImplemented {
				t.Errorf("status = %d, want 501 (non-OpenAI-wire providers are refused before the path is built)", rec.Code)
			}
		})
	}
}

func mustCohere(t *testing.T) providers.Provider {
	t.Helper()
	p, err := coherepkg.New("test-key", "")
	if err != nil {
		t.Fatalf("cohere.New: %v", err)
	}
	return p
}

func mustAzureFoundry(t *testing.T) providers.Provider {
	t.Helper()
	p, err := azurefoundrypkg.New("test-key", "https://probe.services.ai.azure.com", "")
	if err != nil {
		t.Fatalf("azure_foundry.New: %v", err)
	}
	return p
}

// TestPassThroughPreservesEscapedOperationPath guards the RawPath half of the
// strip: a path segment carrying an escaped character keeps its escaping through
// the join, rather than being silently decoded into a different resource id.
func TestPassThroughPreservesEscapedOperationPath(t *testing.T) {
	observed := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed <- r.URL.EscapedPath()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)

	const name = "escaped-path-probe"
	reg := providers.NewRegistry()
	reg.Register(rootShapeProvider{name: name, baseURL: upstream.URL + "/custom/api"})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/models/ft%3Amine", strings.NewReader(`{}`))
	req.Header.Set("X-Provider", name)
	req.ContentLength = 2
	rec := httptest.NewRecorder()
	Handler(reg)(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if got := <-observed; got != "/custom/api/models/ft%3Amine" {
		t.Errorf("upstream escaped path = %q, want /custom/api/models/ft%%3Amine", got)
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

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
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
func (stubSigningProvider) ConfiguredModels() []string     { return nil }
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

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
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

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
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

// TestProxyHandler_ErrorHandler_GenericJSON verifies that when the upstream is
// unreachable the proxy returns a JSON error envelope consistent with the
// project's apierror shape rather than leaking a raw Go error string via
// http.Error (which would set Content-Type: text/plain and expose internals).
func TestProxyHandler_ErrorHandler_GenericJSON(t *testing.T) {
	// Create a server, record its URL, then close it so connections are refused.
	dead := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	reg := buildTestRegistry(t, deadURL)
	handler := Handler(reg)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/files", nil)
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

func TestProxyHandler_StreamingResponseUsesWriteDeadlines(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"id\":\"chunk-1\"}\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	reg := buildTestRegistry(t, upstream.URL)
	handler := Handler(reg)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-4o","stream":true}`))
	req.Header.Set("X-Provider", providerOpenAI)
	req.ContentLength = int64(len(`{"model":"gpt-4o","stream":true}`))
	w := newProxyDeadlineRecorder()

	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "data:") {
		t.Fatalf("expected streamed body, got %q", w.Body.String())
	}
	if len(w.deadlines) < 2 {
		t.Fatalf("expected write deadline set and clear, got %d entries", len(w.deadlines))
	}
	if w.deadlines[0].IsZero() {
		t.Fatal("first deadline should set a timeout")
	}
	if !w.deadlines[len(w.deadlines)-1].IsZero() {
		t.Fatalf("last deadline should clear timeout, got %v", w.deadlines[len(w.deadlines)-1])
	}
	if w.flushes == 0 {
		t.Fatal("expected the reverse proxy to flush through the wrapped ResponseWriter")
	}
}

// Clearing the write deadline after each write also disables http.Server's
// WriteTimeout, so an upstream that sends a chunk and then stalls forever must
// be cut by the idle bound rather than pinning the handler.
func TestProxyHandler_StalledUpstreamIsCutByIdleTimeout(t *testing.T) {
	defer streamio.SetIdleTimeoutForTest(50 * time.Millisecond)()

	upstreamBlocked := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"id\":\"chunk-1\"}\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		// Stall until the gateway gives up on us and cancels the request.
		<-r.Context().Done()
		close(upstreamBlocked)
	}))
	defer upstream.Close()

	handler := Handler(buildTestRegistry(t, upstream.URL))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-4o","stream":true}`))
	req.Header.Set("X-Provider", providerOpenAI)
	req.ContentLength = int64(len(`{"model":"gpt-4o","stream":true}`))

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler(newProxyDeadlineRecorder(), req)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("handler never returned: a stalled upstream pinned the connection")
	}

	select {
	case <-upstreamBlocked:
	case <-time.After(10 * time.Second):
		t.Fatal("upstream request context was never cancelled")
	}
}

// httputil.ReverseProxy runs ModifyResponse before handleUpgradeResponse, which
// requires res.Body to be an io.ReadWriteCloser. Wrapping the body of a 101
// would break every upgrade endpoint (e.g. /v1/realtime).
func TestProxyHandler_SwitchingProtocolsUpgradeStillWorks(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, buf, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("upstream hijack: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_ = buf.Flush()

		line, err := buf.ReadString('\n')
		if err != nil {
			return
		}
		_, _ = buf.WriteString("echo:" + line)
		_ = buf.Flush()
	}))
	defer upstream.Close()

	gateway := httptest.NewServer(Handler(buildTestRegistry(t, upstream.URL)))
	defer gateway.Close()

	var dialer net.Dialer
	conn, err := dialer.DialContext(t.Context(), "tcp", strings.TrimPrefix(gateway.URL, "http://"))
	if err != nil {
		t.Fatalf("dial gateway: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	_, err = conn.Write([]byte("GET /v1/realtime HTTP/1.1\r\nHost: gateway\r\n" +
		"X-Provider: " + providerOpenAI + "\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n"))
	if err != nil {
		t.Fatalf("write upgrade request: %v", err)
	}

	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read status line: %v", err)
	}
	if !strings.Contains(status, "101") {
		t.Fatalf("status = %q, want 101 Switching Protocols", strings.TrimSpace(status))
	}
	for { // drain headers
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read headers: %v", err)
		}
		if strings.TrimSpace(line) == "" {
			break
		}
	}

	// The tunnel must carry bytes both ways.
	if _, err := conn.Write([]byte("ping\n")); err != nil {
		t.Fatalf("write through tunnel: %v", err)
	}
	echo, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read through tunnel: %v", err)
	}
	if strings.TrimSpace(echo) != "echo:ping" {
		t.Fatalf("tunnel echo = %q, want echo:ping", strings.TrimSpace(echo))
	}
}

type proxyDeadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
	flushes   int
}

func newProxyDeadlineRecorder() *proxyDeadlineRecorder {
	return &proxyDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (r *proxyDeadlineRecorder) Flush() {
	r.flushes++
	r.ResponseRecorder.Flush()
}

func (r *proxyDeadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	r.deadlines = append(r.deadlines, deadline)
	return nil
}
