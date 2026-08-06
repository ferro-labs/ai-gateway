package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
	cerebraspkg "github.com/ferro-labs/ai-gateway/providers/cerebras"
	openaipkg "github.com/ferro-labs/ai-gateway/providers/openai"
)

// canary is the string a capture upstream must never see for a model nothing
// owns. The finding this file guards is not a wasted call — it is that the
// request BODY, prompt included, was transmitted to a provider that does not
// serve the model, chosen by registration order.
const canary = "CANARY-do-not-transmit-9f3a"

// captureUpstream is a stub provider endpoint that records whether the canary
// reached it. Assertions read what was received, never a call count.
type captureUpstream struct {
	*httptest.Server
	mu   sync.Mutex
	seen []string
}

func newCaptureUpstream(t *testing.T) *captureUpstream {
	t.Helper()
	c := &captureUpstream{}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		c.mu.Lock()
		c.seen = append(c.seen, string(buf[:n]))
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(c.Close)
	return c
}

func (c *captureUpstream) sawCanary() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, body := range c.seen {
		if strings.Contains(body, canary) {
			return true
		}
	}
	return false
}

// indexedSource is the answer *aigateway.Gateway gives, in the shape
// providers.ProviderSource asks for: a model resolves only to the provider an
// index records as its owner, and a name no entry covers resolves to nobody.
//
// The Gateway builds that index from the catalog, live discovery and the models
// this instance's config named; reproducing it here would mean loading a
// catalog into a package whose TestMain is a goroutine-leak check. What the
// proxy owns is what it does with the two answers, which is what these tests
// pin. The Gateway's own resolution is covered by gateway_model_ownership_test.go.
type indexedSource struct {
	byName map[string]providers.Provider
	owners map[string]string
}

var _ providers.ProviderSource = (*indexedSource)(nil)

func (s *indexedSource) Get(name string) (providers.Provider, bool) {
	p, ok := s.byName[name]
	return p, ok
}

func (s *indexedSource) List() []string {
	names := make([]string, 0, len(s.byName))
	for name := range s.byName {
		names = append(names, name)
	}
	return names
}

func (s *indexedSource) ModelsFor(string) []providers.ModelInfo { return nil }

func (s *indexedSource) AllModels() []providers.ModelInfo { return nil }

func (s *indexedSource) FindByModel(model string) (providers.Provider, bool) {
	owner, ok := s.owners[model]
	if !ok {
		return nil, false
	}
	p, ok := s.byName[owner]
	return p, ok
}

// twoProviderSource wires cerebras and openai at their own capture upstreams.
// cerebras is the shape that caused the finding: its SupportsModel is
// `return true` and it registers before openai, so a registry-backed
// pass-through resolved BOTH an unowned model and gpt-4o to it.
func twoProviderSource(t *testing.T, cerebrasUp, openaiUp string) *indexedSource {
	t.Helper()
	cer, err := cerebraspkg.New("sk-cerebras-test", cerebrasUp)
	if err != nil {
		t.Fatalf("cerebras.New: %v", err)
	}
	oai, err := openaipkg.New("sk-openai-test", openaiUp)
	if err != nil {
		t.Fatalf("openai.New: %v", err)
	}
	return &indexedSource{
		byName: map[string]providers.Provider{"cerebras": cer, providerOpenAI: oai},
		owners: map[string]string{"gpt-4o": providerOpenAI, "llama3.1-8b": "cerebras"},
	}
}

func proxyPost(t *testing.T, h http.HandlerFunc, body string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h(w, req)
	return w
}

func assertErrorCode(t *testing.T, w *httptest.ResponseRecorder, wantStatus int, wantCode string) {
	t.Helper()
	if w.Code != wantStatus {
		t.Errorf("status = %d, want %d (body %s)", w.Code, wantStatus, w.Body.String())
	}
	var env struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if env.Error.Code != wantCode {
		t.Errorf("error.code = %q, want %q", env.Error.Code, wantCode)
	}
}

// TestHandler_UnownedModelIsNotTransmitted is the finding. A model no target
// owns used to resolve through the registry to the first provider whose
// advisory SupportsModel said true, and the pass-through forwarded the body
// there verbatim. Nothing may be transmitted now.
func TestHandler_UnownedModelIsNotTransmitted(t *testing.T) {
	cerebrasUp, openaiUp := newCaptureUpstream(t), newCaptureUpstream(t)
	src := twoProviderSource(t, cerebrasUp.URL, openaiUp.URL)

	w := proxyPost(t, Handler(src), `{"model":"ghost-model-p1","input":"`+canary+`"}`, nil)

	// 404, the same answer the routed surfaces give for the same condition —
	// see apierror.WriteModelNotFound.
	assertErrorCode(t, w, http.StatusNotFound, "model_not_found")
	for name, up := range map[string]*captureUpstream{"cerebras": cerebrasUp, providerOpenAI: openaiUp} {
		if up.sawCanary() {
			t.Errorf("%s upstream received the canary body for a model it does not own", name)
		}
	}
}

// TestHandler_OwnedModelReachesItsOwner is the other half: gating ownership
// must not cost the pass-through its purpose. gpt-4o is openai's, and under the
// registry it went to cerebras — first registered, SupportsModel `return true`.
func TestHandler_OwnedModelReachesItsOwner(t *testing.T) {
	cerebrasUp, openaiUp := newCaptureUpstream(t), newCaptureUpstream(t)
	src := twoProviderSource(t, cerebrasUp.URL, openaiUp.URL)

	w := proxyPost(t, Handler(src), `{"model":"gpt-4o","input":"`+canary+`"}`, nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if !openaiUp.sawCanary() {
		t.Error("openai upstream never received the request for a model it owns")
	}
	if cerebrasUp.sawCanary() {
		t.Error("cerebras upstream received a request for a model openai owns")
	}
	if got := w.Header().Get("X-Gateway-Provider"); got != providerOpenAI {
		t.Errorf("X-Gateway-Provider = %q, want %q", got, providerOpenAI)
	}
}

// TestHandler_XProviderForwardsUnownedModel pins the deliberate exemption. The
// header is the caller naming the backend, not the gateway guessing one, and it
// is the only way to reach a pass-through endpoint whose model id no index can
// enumerate. Gating it would leave those unreachable.
func TestHandler_XProviderForwardsUnownedModel(t *testing.T) {
	cerebrasUp, openaiUp := newCaptureUpstream(t), newCaptureUpstream(t)
	src := twoProviderSource(t, cerebrasUp.URL, openaiUp.URL)

	w := proxyPost(t, Handler(src),
		`{"model":"ft:some-private-tune","input":"`+canary+`"}`,
		map[string]string{"X-Provider": "cerebras"})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if !cerebrasUp.sawCanary() {
		t.Error("cerebras upstream never received the explicitly addressed request")
	}
	if openaiUp.sawCanary() {
		t.Error("openai upstream received a request addressed to cerebras")
	}
}

// TestHandler_NoModelStillAsksForProvider keeps the pass-through's own shape.
// Most endpoints it exists for — /v1/files, /v1/batches, /v1/models/{id} —
// carry no model to route on, so "you named no model" stays its own answer and
// must not become model_not_found.
func TestHandler_NoModelStillAsksForProvider(t *testing.T) {
	cerebrasUp, openaiUp := newCaptureUpstream(t), newCaptureUpstream(t)
	src := twoProviderSource(t, cerebrasUp.URL, openaiUp.URL)

	w := proxyPost(t, Handler(src), `{"purpose":"assistants","input":"`+canary+`"}`, nil)

	assertErrorCode(t, w, http.StatusBadRequest, "provider_not_resolved")
	if cerebrasUp.sawCanary() || openaiUp.sawCanary() {
		t.Error("an unaddressed request was transmitted upstream")
	}
}

// TestRegistrySatisfiesProviderSource records that widening Handler's parameter
// did not break the existing call shape: every caller that passed a
// *providers.Registry still compiles, it simply keeps the registry's weaker
// answer. See providers.Registry.FindByModel.
func TestRegistrySatisfiesProviderSource(t *testing.T) {
	var src providers.ProviderSource = buildTestRegistry(t, "http://localhost")
	if _, ok := src.Get(providerOpenAI); !ok {
		t.Fatal("registry-backed source lost the provider it holds")
	}
}
