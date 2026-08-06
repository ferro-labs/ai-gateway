package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/providers"
)

func TestListProviders_NilRegistry(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	// h.Providers is nil by default in setupTestRouter.
	req := authedRequest(http.MethodGet, "/admin/providers", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var result []any
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty providers list, got %d", len(result))
	}
}

// catalogTestProvider is the minimal core.Provider needed to register a name.
type catalogTestProvider struct{ name string }

func (p catalogTestProvider) Name() string            { return p.name }
func (catalogTestProvider) SupportsModel(string) bool { return false }
func (catalogTestProvider) Complete(context.Context, providers.Request) (*providers.Response, error) {
	return nil, nil
}

// TestListProviderCatalog asserts the endpoint reports every built-in provider
// (not only registered ones), flags which are registered, and reports a
// per-provider non-deprecated catalog model count.
func TestListProviderCatalog(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	// Only "openai" is registered; the catalog carries one active openai model
	// and one deprecated one, so the reported count must be 1.
	h.Providers = registryWith(catalogTestProvider{name: "openai"})
	h.Catalog = func() models.Catalog {
		return models.Catalog{
			"openai/gpt-4o":  {Provider: "openai", ModelID: "gpt-4o"},
			"openai/retired": {Provider: "openai", ModelID: "retired", Lifecycle: models.Lifecycle{Status: "deprecated"}},
		}
	}

	req := authedRequest(http.MethodGet, "/admin/providers/catalog", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Providers []struct {
			ID            string `json:"id"`
			Registered    bool   `json:"registered"`
			CatalogModels int    `json:"catalog_models"`
		} `json:"providers"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Every built-in appears, registered or not.
	if len(body.Providers) != len(providers.AllProviders()) {
		t.Fatalf("got %d providers, want all %d built-ins", len(body.Providers), len(providers.AllProviders()))
	}

	seen := make(map[string]struct{}, len(body.Providers))
	var openai, anthropic *struct {
		ID            string `json:"id"`
		Registered    bool   `json:"registered"`
		CatalogModels int    `json:"catalog_models"`
	}
	for i := range body.Providers {
		seen[body.Providers[i].ID] = struct{}{}
		switch body.Providers[i].ID {
		case "openai":
			openai = &body.Providers[i]
		case "anthropic":
			anthropic = &body.Providers[i]
		}
	}
	if len(seen) != len(body.Providers) {
		t.Errorf("response contains duplicate provider ids")
	}
	if openai == nil {
		t.Fatal("openai missing from the roster")
	}
	if !openai.Registered {
		t.Error("openai should be registered")
	}
	if openai.CatalogModels != 1 {
		t.Errorf("openai catalog_models = %d, want 1 (deprecated excluded)", openai.CatalogModels)
	}
	if anthropic == nil {
		t.Fatal("anthropic missing from the roster")
	}
	if anthropic.Registered {
		t.Error("anthropic should not be registered (no credential in this test)")
	}
}
