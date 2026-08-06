//go:build integration
// +build integration

package http_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// proxyCanary is the string the capture upstream must never see. These tests
// assert on what was transmitted, not on how many calls were made: the finding
// is that the request body reached a provider that does not serve the model.
const proxyCanary = "CANARY-passthrough-must-not-egress"

// claimsEveryModelProvider is the shape most credentialed providers ship:
// SupportsModel answers true for every id because the upstream's inventory is
// not theirs to enumerate. It is proxiable, so a pass-through that resolves
// through the registry will forward a body to it for any model at all.
type claimsEveryModelProvider struct {
	name    string
	baseURL string

	mu   sync.Mutex
	seen []string
}

var _ core.ProxiableProvider = (*claimsEveryModelProvider)(nil)

func (p *claimsEveryModelProvider) Name() string              { return p.name }
func (p *claimsEveryModelProvider) SupportsModel(string) bool { return true }
func (p *claimsEveryModelProvider) BaseURL() string           { return p.baseURL }

func (p *claimsEveryModelProvider) AuthHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer sk-claims-everything"}
}

func (p *claimsEveryModelProvider) Complete(context.Context, core.Request) (*core.Response, error) {
	return nil, core.ErrNoCapableProvider
}

func (p *claimsEveryModelProvider) sawCanary() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, body := range p.seen {
		if strings.Contains(body, proxyCanary) {
			return true
		}
	}
	return false
}

// registerClaimant adds a wildcard-claiming provider to both the registry and
// the gateway, which is what bootstrap.BuildGateway does with every provider a
// credential registered.
//
// It is what separates the two sources. The registry resolves a model by
// walking advisory SupportsModel answers in registration order, so this
// provider claims every name the stub does not and receives the body. The
// gateway resolves from its routing index, where this provider owns nothing:
// it names no configured model, appears in no catalog, and declares no
// core.AnyModelProvider. A request that lands here proves the pass-through is
// still resolving through the registry.
func registerClaimant(t *testing.T, env *testEnv) *claimsEveryModelProvider {
	t.Helper()
	p := &claimsEveryModelProvider{name: "claims-everything"}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		p.mu.Lock()
		p.seen = append(p.seen, string(buf[:n]))
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(upstream.Close)
	p.baseURL = upstream.URL
	env.Registry.Register(p)
	env.Gateway.RegisterProvider(p)

	// Name the provider in the config as well as registering it. Registration
	// follows credentials and routing follows the config, so a provider present
	// in only the first is one the pass-through refuses to send a body to — a
	// state production does not produce for a provider an operator intends to
	// reach. Ownership is still not granted here: the claimant owns no model
	// any of these tests names, which is what keeps the ownership assertions
	// meaningful.
	if err := env.Gateway.ReloadConfig(t.Context(), config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets:  []config.Target{{VirtualKey: "stub"}, {VirtualKey: p.name}},
	}); err != nil {
		t.Fatalf("registerClaimant: reload config: %v", err)
	}
	return p
}

// TestProxy_UnownedModelIsNotTransmitted drives the fully wired router. A
// /v1/* path the gateway does not handle natively, carrying a model no target
// owns, must be refused before anything is sent upstream.
func TestProxy_UnownedModelIsNotTransmitted(t *testing.T) {
	env := newTestServer(t)
	claimant := registerClaimant(t, env)

	req := newTestRequest(t, http.MethodPost, env.Server.URL+"/v1/responses",
		strings.NewReader(`{"model":"ghost-model-p1","input":"`+proxyCanary+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testMasterKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	// 404, the answer every routed surface gives for a model this gateway does
	// not serve — see apierror.WriteModelNotFound.
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	assertOpenAIError(t, resp.Body, "invalid_request_error", "model_not_found")

	if claimant.sawCanary() {
		t.Error("the request body was transmitted to a provider that owns no model in the routing index")
	}
}

// TestProxy_XProviderStillForwards keeps the escape hatch working: an explicit
// header is the caller naming the backend, and is not gated on ownership.
func TestProxy_XProviderStillForwards(t *testing.T) {
	env := newTestServer(t)
	claimant := registerClaimant(t, env)

	req := newTestRequest(t, http.MethodPost, env.Server.URL+"/v1/responses",
		strings.NewReader(`{"model":"ghost-model-p1","input":"`+proxyCanary+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Provider", claimant.name)
	req.Header.Set("Authorization", "Bearer "+testMasterKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !claimant.sawCanary() {
		t.Error("an explicitly addressed pass-through request never reached its provider")
	}
}

// TestProxy_OwnedModelStillForwards proves the gate did not close on the
// pass-through's legitimate use: a model the gateway's index owns still routes
// to its owner over a path the gateway does not natively handle.
func TestProxy_OwnedModelStillForwards(t *testing.T) {
	var upstreamPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	env := newTestServer(t)
	env.Stub.SetBaseURL(upstream.URL + "/v1")
	registerClaimant(t, env)

	req := newTestRequest(t, http.MethodPost, env.Server.URL+"/v1/responses",
		strings.NewReader(`{"model":"`+stubModelName+`","input":"`+proxyCanary+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testMasterKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Gateway-Provider"); got != "stub" {
		t.Errorf("X-Gateway-Provider = %q, want %q", got, "stub")
	}
	if !strings.HasPrefix(upstreamPath, "/v1/responses") {
		t.Errorf("upstream path = %q, want prefix /v1/responses", upstreamPath)
	}
}
