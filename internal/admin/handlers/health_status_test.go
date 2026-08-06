package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/admin/repository"
	"github.com/ferro-labs/ai-gateway/internal/requestlog"
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
			store := repository.NewKeyStore()
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
				Status     string `json:"status"`
				Components []struct {
					Name   string `json:"name"`
					Status string `json:"status"`
				} `json:"components"`
			}
			if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
				t.Fatalf("decode health response: %v", err)
			}
			if payload.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", payload.Status, tt.wantStatus)
			}
			wantComponents := map[string]string{
				"API": "healthy", "Key store": "healthy",
				"Config store": "disabled", "Request logs": "disabled",
				// No Audit store on this Handlers, so it reports disabled like
				// the other unset stores. It never downgrades overall status.
				"Audit log": "disabled",
			}
			if len(payload.Components) != len(wantComponents) {
				t.Fatalf("components = %v, want %d entries", payload.Components, len(wantComponents))
			}
			for _, component := range payload.Components {
				if want := wantComponents[component.Name]; component.Status != want {
					t.Errorf("component %q status = %q, want %q", component.Name, component.Status, want)
				}
			}
		})
	}
}

// TestHealthCheckProbesRequestLogStore proves the request log store is asked
// whether it is reachable rather than reported healthy on the strength of being
// configured. An unreachable log database is invisible otherwise, unlike every
// other component in the same list.
func TestHealthCheckProbesRequestLogStore(t *testing.T) {
	tests := []struct {
		name          string
		logs          requestlog.Reader
		wantComponent string
		wantOverall   string
	}{
		{name: "not configured", logs: nil, wantComponent: "disabled", wantOverall: "healthy"},
		{name: "reachable", logs: &fakeLogReader{}, wantComponent: "healthy", wantOverall: "healthy"},
		{name: "unreachable", logs: unreachableLogReader{}, wantComponent: "unavailable", wantOverall: "degraded"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := repository.NewKeyStore()
			h := &Handlers{Keys: store, Providers: registryWith(adminHealthProvider{}), Logs: tt.logs}
			r := chi.NewRouter()
			r.Use(AuthMiddleware(store, ""))
			r.Mount("/admin", h.Routes())
			key := createAdminKey(t, h)

			req := authedRequest(http.MethodGet, "/admin/health", "", key)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			var payload struct {
				Status     string `json:"status"`
				Components []struct {
					Name   string `json:"name"`
					Status string `json:"status"`
				} `json:"components"`
			}
			decodeJSON(t, w.Body, &payload)

			got := ""
			for _, component := range payload.Components {
				if component.Name == "Request logs" {
					got = component.Status
				}
			}
			if got != tt.wantComponent {
				t.Errorf("request logs status = %q, want %q", got, tt.wantComponent)
			}
			if payload.Status != tt.wantOverall {
				t.Errorf("overall status = %q, want %q", payload.Status, tt.wantOverall)
			}
		})
	}
}

// unreachableLogReader is a configured request log store whose database is down.
type unreachableLogReader struct{}

func (unreachableLogReader) List(context.Context, requestlog.Query) (requestlog.ListResult, error) {
	return requestlog.ListResult{}, errors.New("request log store unreachable")
}

func (unreachableLogReader) Stats(context.Context, requestlog.Query) (requestlog.StatsResult, error) {
	return requestlog.StatsResult{}, errors.New("request log store unreachable")
}

func registryWith(p providers.Provider) *providers.Registry {
	registry := providers.NewRegistry()
	registry.Register(p)
	return registry
}

// ghostProviderSource names a provider that cannot be resolved, the state the
// health handler reports as "degraded".
type ghostProviderSource struct{}

func (ghostProviderSource) List() []string                         { return []string{"ghost"} }
func (ghostProviderSource) Get(string) (providers.Provider, bool)  { return nil, false }
func (ghostProviderSource) AllModels() []providers.ModelInfo       { return nil }
func (ghostProviderSource) ModelsFor(string) []providers.ModelInfo { return nil }
func (ghostProviderSource) FindByModel(string) (providers.Provider, bool) {
	return nil, false
}

type adminHealthProvider struct{}

func (adminHealthProvider) Name() string               { return "admin-health" }
func (adminHealthProvider) ConfiguredModels() []string { return []string{"admin-health-model"} }
func (adminHealthProvider) SupportsModel(model string) bool {
	return model == "admin-health-model"
}
func (adminHealthProvider) Models() []providers.ModelInfo {
	return []providers.ModelInfo{{ID: "admin-health-model", Object: "model", OwnedBy: "admin-health"}}
}
func (adminHealthProvider) Complete(context.Context, providers.Request) (*providers.Response, error) {
	return nil, nil
}
