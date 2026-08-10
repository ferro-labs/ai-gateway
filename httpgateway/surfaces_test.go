package httpgateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/models"
)

func TestEmbeddingHandlersDelegateToOwnedSurfaces(t *testing.T) {
	t.Setenv(models.CatalogURLEnv, "file:///ferro-tests-use-embedded-catalog")
	gw, err := aigateway.New(config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "openai"}},
	})
	if err != nil {
		t.Fatalf("create gateway: %v", err)
	}
	defer func() { _ = gw.Close() }()

	tests := []struct {
		name    string
		handler http.Handler
		request *http.Request
		status  int
		// wantBody is a token unique to the intended internal handler, so a wrapper
		// wired to the wrong surface (e.g. Batch and ResponsesStateful both answer
		// 501) is caught rather than passing on the shared status alone.
		wantBody string
	}{
		{"generic pass-through", Passthrough(gw), httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/fine_tuning/jobs", strings.NewReader(`{}`)), http.StatusBadRequest, "provider_not_resolved"},
		{"batch fixed target", Batch(gw), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/files", nil), http.StatusNotImplemented, "batch_not_configured"},
		{"responses create", ResponsesCreate(gw), httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/responses", strings.NewReader(`{}`)), http.StatusBadRequest, "model is required"},
		{"responses stateful fixed target", ResponsesStateful(gw), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/responses/resp_1", nil), http.StatusNotImplemented, "responses_not_configured"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tt.handler.ServeHTTP(rec, tt.request)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.status, rec.Body.String())
			}
			if body := rec.Body.String(); !strings.Contains(body, tt.wantBody) {
				t.Fatalf("body = %q, want it to contain %q (wrapper may delegate to the wrong handler)", body, tt.wantBody)
			}
		})
	}
}
