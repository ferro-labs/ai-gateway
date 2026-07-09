package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/go-chi/chi/v5"
)

// /admin/health is bearer-authenticated and is read by the dashboard login probe,
// the providers page, and `ferrogw admin health` — all of which treat a non-2xx
// as a failure. It therefore always answers 200 and reports state in the body.
func TestHealthCheckAlwaysReturns200AndReportsStatusInBody(t *testing.T) {
	tests := []struct {
		name       string
		providers  providers.ProviderSource
		wantStatus string
	}{
		{name: "healthy", providers: registryWith(adminHealthProvider{}), wantStatus: "healthy"},
		{name: "no providers", providers: providers.NewRegistry(), wantStatus: "no_providers"},
		{name: "degraded", providers: ghostProviderSource{}, wantStatus: "degraded"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewKeyStore()
			h := &Handlers{Keys: store, Providers: tt.providers}
			r := chi.NewRouter()
			r.Use(AuthMiddleware(store, ""))
			r.Mount("/admin", h.Routes())
			key := createAdminKey(t, h)

			req := authedRequest(http.MethodGet, "/admin/health", "", key)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status code = %d, want 200: %s", w.Code, w.Body.String())
			}
			var payload struct {
				Status string `json:"status"`
			}
			if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
				t.Fatalf("decode health response: %v", err)
			}
			if payload.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", payload.Status, tt.wantStatus)
			}
		})
	}
}

func registryWith(p providers.Provider) *providers.Registry {
	registry := providers.NewRegistry()
	registry.Register(p)
	return registry
}

// ghostProviderSource names a provider that cannot be resolved, the state the
// health handler reports as "degraded".
type ghostProviderSource struct{}

func (ghostProviderSource) List() []string                        { return []string{"ghost"} }
func (ghostProviderSource) Get(string) (providers.Provider, bool) { return nil, false }
func (ghostProviderSource) AllModels() []providers.ModelInfo      { return nil }
func (ghostProviderSource) FindByModel(string) (providers.Provider, bool) {
	return nil, false
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
