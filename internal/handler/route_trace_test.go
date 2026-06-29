package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/routetrace"
	"github.com/ferro-labs/ai-gateway/providers"
)

// traceMockProvider is a providers.Provider test double for the route-trace
// handler tests. Complete must NEVER be invoked by the tracer.
type traceMockProvider struct {
	name   string
	models []string
	calls  int
}

func (m *traceMockProvider) Name() string                  { return m.name }
func (m *traceMockProvider) SupportedModels() []string     { return m.models }
func (m *traceMockProvider) Models() []providers.ModelInfo { return nil }
func (m *traceMockProvider) SupportsModel(model string) bool {
	for _, mm := range m.models {
		if mm == model {
			return true
		}
	}
	return false
}
func (m *traceMockProvider) Complete(_ context.Context, _ providers.Request) (*providers.Response, error) {
	m.calls++
	return nil, errTraceCompleteCalled
}

var errTraceCompleteCalled = errors.New("traceMockProvider.Complete must never be called in a dry-run trace")

// newTraceGateway builds a fallback-strategy gateway with two providers that
// both support a model present in the bundled catalog (gpt-3.5-turbo).
func newTraceGateway(t *testing.T) *aigateway.Gateway {
	t.Helper()
	gw, err := aigateway.New(aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeFallback},
		Targets: []aigateway.Target{
			{VirtualKey: "openai"},
			{VirtualKey: "ollama-cloud"},
		},
	})
	if err != nil {
		t.Fatalf("aigateway.New: %v", err)
	}
	gw.RegisterProvider(&traceMockProvider{name: "openai", models: []string{"gpt-3.5-turbo"}})
	gw.RegisterProvider(&traceMockProvider{name: "ollama-cloud", models: []string{"gpt-3.5-turbo", "qwen3.5:397b-cloud"}})
	return gw
}

func TestRouteTrace_FallbackSelectsFirstSupportingTarget(t *testing.T) {
	gw := newTraceGateway(t)
	h := RouteTrace(gw)

	body := `{"model":"gpt-3.5-turbo","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/route/trace", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// No provider.Complete call must occur during a dry-run.
	for _, name := range []string{"openai", "ollama-cloud"} {
		p, _ := gw.Get(name)
		if m, ok := p.(*traceMockProvider); ok && m.calls != 0 {
			t.Errorf("%s.Complete called %d times; tracer must never call a provider", name, m.calls)
		}
	}

	var resp routetrace.TraceResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v; body=%s", err, rec.Body.String())
	}
	if !resp.DryRun {
		t.Error("response.DryRun must be true")
	}
	if resp.Strategy != "fallback" {
		t.Errorf("response.Strategy = %q, want fallback", resp.Strategy)
	}
	if resp.RequestedModel != "gpt-3.5-turbo" || resp.ResolvedModel != "gpt-3.5-turbo" {
		t.Errorf("model req/resolved = %q/%q, want gpt-3.5-turbo/gpt-3.5-turbo", resp.RequestedModel, resp.ResolvedModel)
	}
	if resp.SelectedTargetKey != "openai" {
		t.Errorf("SelectedTargetKey = %q, want openai (first supporting target)", resp.SelectedTargetKey)
	}
	if !resp.Catalog.ModelFound {
		t.Errorf("Catalog.ModelFound = false, want true (gpt-3.5-turbo is in the bundled catalog)")
	}
	if len(resp.CandidateTargets) != 2 {
		t.Fatalf("len(CandidateTargets) = %d, want 2", len(resp.CandidateTargets))
	}
	if !resp.CandidateTargets[0].SupportsModel {
		t.Error("first candidate SupportsModel must be true")
	}
}

func TestRouteTrace_UnknownModelHasNoSelection(t *testing.T) {
	gw := newTraceGateway(t)
	h := RouteTrace(gw)

	body := `{"model":"not-a-real-model"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/route/trace", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (trace still returns, just no selection); body=%s", rec.Code, rec.Body.String())
	}
	var resp routetrace.TraceResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.SelectedTargetKey != "" {
		t.Errorf("SelectedTargetKey = %q, want empty (no provider supports the model)", resp.SelectedTargetKey)
	}
	for _, c := range resp.CandidateTargets {
		if c.SupportsModel {
			t.Errorf("candidate %q SupportsModel must be false for an unknown model", c.TargetKey)
		}
	}
	if resp.Catalog.ModelFound {
		t.Error("Catalog.ModelFound must be false for an unknown model")
	}
}

func TestRouteTrace_MissingModelIs400(t *testing.T) {
	gw := newTraceGateway(t)
	h := RouteTrace(gw)

	req := httptest.NewRequest(http.MethodPost, "/v1/route/trace", strings.NewReader(`{"messages":[]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for missing model; body=%s", rec.Code, rec.Body.String())
	}
}

func TestRouteTrace_MalformedJSONIs400(t *testing.T) {
	gw := newTraceGateway(t)
	h := RouteTrace(gw)

	req := httptest.NewRequest(http.MethodPost, "/v1/route/trace", strings.NewReader(`{not json`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for malformed JSON; body=%s", rec.Code, rec.Body.String())
	}
}
