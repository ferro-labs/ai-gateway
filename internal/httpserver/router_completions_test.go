package httpserver_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/internal/admin/repository"
	"github.com/ferro-labs/ai-gateway/internal/httpserver"
	"github.com/ferro-labs/ai-gateway/providers"
)

// /v1/completions is routed through the gateway like its siblings, not served
// straight off the provider registry. The registry holds every provider whose
// credential is configured, so a route reading from it answered for providers
// an operator had deliberately left out of `targets` — and with none of the
// plugins, strategy, or limits that reach a request through the gateway.
func TestRouter_CompletionsRoutesThroughGateway(t *testing.T) {
	t.Setenv("ALLOW_UNAUTHENTICATED_PROXY", "true")

	// The gateway routes to "listed" only; the registry additionally knows the
	// stub provider, standing in for a credential that is configured but
	// excluded from targets.
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets:  []config.Target{{VirtualKey: "listed"}},
	})
	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}

	reg := providers.NewRegistry()
	reg.Register(stubProvider{})
	r := httpserver.NewRouter(reg, repository.NewKeyStore(), nil, nil, gw, nil, nil, nil, nil, nil, "", nil)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/completions",
		strings.NewReader(`{"model":"stub-model","prompt":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code == http.StatusOK {
		t.Fatalf("a provider known only to the registry was served: %s", w.Body.String())
	}
}
