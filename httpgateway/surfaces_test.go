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
	}{
		{"generic pass-through", Passthrough(gw), httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/fine_tuning/jobs", strings.NewReader(`{}`)), http.StatusBadRequest},
		{"batch fixed target", Batch(gw), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/files", nil), http.StatusNotImplemented},
		{"responses create", ResponsesCreate(gw), httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/responses", strings.NewReader(`{}`)), http.StatusBadRequest},
		{"responses stateful fixed target", ResponsesStateful(gw), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/responses/resp_1", nil), http.StatusNotImplemented},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tt.handler.ServeHTTP(rec, tt.request)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.status, rec.Body.String())
			}
		})
	}
}
