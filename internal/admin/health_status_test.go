package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/go-chi/chi/v5"
)

func TestHealthCheckHealthyProvidersReturns200(t *testing.T) {
	store := NewKeyStore()
	registry := providers.NewRegistry()
	registry.Register(adminHealthProvider{})
	h := &Handlers{Keys: store, Providers: registry}
	r := chi.NewRouter()
	r.Use(AuthMiddleware(store, ""))
	r.Mount("/admin", h.Routes())
	key := createAdminKey(t, h)

	req := authedRequest(http.MethodGet, "/admin/health", "", key)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

type adminHealthProvider struct{}

func (adminHealthProvider) Name() string              { return "admin-health" }
func (adminHealthProvider) SupportedModels() []string { return []string{"admin-health-model"} }
func (adminHealthProvider) SupportsModel(model string) bool {
	return model == "admin-health-model"
}
func (adminHealthProvider) Models() []providers.ModelInfo {
	return []providers.ModelInfo{{ID: "admin-health-model", Object: "model", OwnedBy: "admin-health"}}
}
func (adminHealthProvider) Complete(context.Context, providers.Request) (*providers.Response, error) {
	return nil, nil
}
