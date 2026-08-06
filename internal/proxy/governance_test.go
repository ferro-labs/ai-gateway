package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"

	_ "github.com/ferro-labs/ai-gateway/plugin/logger"
	_ "github.com/ferro-labs/ai-gateway/plugin/maxtoken"
	_ "github.com/ferro-labs/ai-gateway/plugin/wordfilter"
)

// upstreamCredential is the value the stub provider injects. A test that
// asserts the upstream was never contacted is asserting this never travelled.
// Assembled rather than written whole so it is not mistaken for a real secret
// by a scanner.
var upstreamCredential = "Bearer " + "sk-" + "gateway-injected"

// proxiableStub is a provider the pass-through can actually forward to: it
// speaks the OpenAI wire (no NonOpenAIWireProvider marker) and carries a base
// URL and auth headers.
type proxiableStub struct {
	name    string
	baseURL string
	models  []string
}

func (p *proxiableStub) Name() string { return p.name }

func (p *proxiableStub) SupportsModel(model string) bool {
	for _, m := range p.models {
		if m == model {
			return true
		}
	}
	return false
}

func (p *proxiableStub) Complete(context.Context, core.Request) (*core.Response, error) {
	return nil, nil
}

func (p *proxiableStub) BaseURL() string { return p.baseURL }

func (p *proxiableStub) AuthHeaders() map[string]string {
	return map[string]string{"Authorization": upstreamCredential}
}

var (
	_ providers.Provider          = (*proxiableStub)(nil)
	_ providers.ProxiableProvider = (*proxiableStub)(nil)
)

// countingUpstream is a stub provider API that records every request that
// reached it, so a test can assert a blocked body never left the process.
type countingUpstream struct {
	*httptest.Server
	hits atomic.Int32
	// lastBody is the body the last request carried, so a test can prove the
	// content itself travelled rather than only that a request did.
	lastBody atomic.Value
}

func newCountingUpstream(t *testing.T) *countingUpstream {
	t.Helper()
	up := &countingUpstream{}
	up.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up.hits.Add(1)
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		up.lastBody.Store(string(buf[:n]))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response"}`))
	}))
	t.Cleanup(up.Close)
	return up
}

// wordFilterConfig is a before_request guardrail blocking one word.
func wordFilterConfig(word string) config.PluginConfig {
	return config.PluginConfig{
		Name:    "word-filter",
		Type:    "guardrail",
		Stage:   "before_request",
		Enabled: true,
		Config:  map[string]any{"blocked_words": []string{word}},
	}
}

// maxTokenConfig is a before_request guardrail that reads no request content
// unless max_input_length is set. Pass 0 to leave it unset.
func maxTokenConfig(maxInputLength int) config.PluginConfig {
	settings := map[string]any{"max_tokens": 4096}
	if maxInputLength > 0 {
		settings["max_input_length"] = maxInputLength
	}
	return config.PluginConfig{
		Name:    "max-token",
		Type:    "guardrail",
		Stage:   "before_request",
		Enabled: true,
		Config:  settings,
	}
}

// newGovernedGateway builds a gateway whose single target is a proxiable stub
// pointed at upstream, with the supplied plugins loaded.
func newGovernedGateway(t *testing.T, upstream string, plugins ...config.PluginConfig) *aigateway.Gateway {
	t.Helper()
	// Keep New() off the network: the catalog fetch runs during construction.
	t.Setenv("FERRO_MODEL_CATALOG_TIMEOUT", "0")

	cfg := config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "stub", Models: []string{"stub-model"}}},
		Plugins:  plugins,
	}
	gw, err := aigateway.New(cfg)
	if err != nil {
		t.Fatalf("aigateway.New: %v", err)
	}
	t.Cleanup(func() { _ = gw.Close() })
	gw.RegisterProvider(&proxiableStub{name: "stub", baseURL: upstream, models: []string{"stub-model"}})
	if err := gw.LoadPlugins(); err != nil {
		t.Fatalf("LoadPlugins: %v", err)
	}
	return gw
}

// passthroughPOST drives one pass-through request through the wildcard handler.
func passthroughPOST(t *testing.T, gw *aigateway.Gateway, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	Handler(gw)(w, r)
	return w
}

// CORE-002: a configured content guardrail applies to the /v1/* pass-through,
// so the content it blocks on /v1/chat/completions it also blocks on
// /v1/responses — and the upstream is never contacted, so the blocked prompt
// never travels under the gateway's own provider credential.
func TestPassthrough_GuardrailBlocksBlockedContent(t *testing.T) {
	up := newCountingUpstream(t)
	gw := newGovernedGateway(t, up.URL, wordFilterConfig("forbidden"))

	w := passthroughPOST(t, gw, "/v1/responses",
		`{"model":"stub-model","input":"tell me the forbidden thing"}`, nil)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (guardrail rejection); body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
	if hits := up.hits.Load(); hits != 0 {
		t.Errorf("upstream received %d requests, want 0 — the blocked body was forwarded with the gateway credential", hits)
	}
}

// The same guardrail must apply when the caller names the provider explicitly.
// X-Provider is the documented escape hatch for a model no index can enumerate;
// it was also the way past every guardrail.
func TestPassthrough_GuardrailAppliesUnderXProvider(t *testing.T) {
	up := newCountingUpstream(t)
	gw := newGovernedGateway(t, up.URL, wordFilterConfig("forbidden"))

	w := passthroughPOST(t, gw, "/v1/responses",
		`{"model":"some-unenumerable-model","input":"the forbidden thing"}`,
		map[string]string{"X-Provider": "stub"})

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d; body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
	if hits := up.hits.Load(); hits != 0 {
		t.Errorf("upstream received %d requests, want 0", hits)
	}
}

// A body the guardrail passes is still forwarded: governance must not break the
// coverage the pass-through exists for.
func TestPassthrough_CleanBodyIsForwarded(t *testing.T) {
	up := newCountingUpstream(t)
	gw := newGovernedGateway(t, up.URL, wordFilterConfig("forbidden"))

	w := passthroughPOST(t, gw, "/v1/responses",
		`{"model":"stub-model","input":"a perfectly ordinary prompt"}`, nil)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if hits := up.hits.Load(); hits != 1 {
		t.Errorf("upstream received %d requests, want 1", hits)
	}
	if got := w.Header().Get("X-Gateway-Provider"); got != "stub" {
		t.Errorf("X-Gateway-Provider = %q, want %q", got, "stub")
	}
}
