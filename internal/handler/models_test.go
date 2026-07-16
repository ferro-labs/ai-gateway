package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/models"
)

// TestModels_EnrichesAliasedCatalogTierInstance verifies that GET /v1/models
// enriches (non-zero pricing/context-window) a model surfaced through
// AllModels()'s catalog-precedence tier for an aliased provider instance
// (e.g. a second Ollama Cloud account registered under a distinct routing
// alias), while OwnedBy still reports the alias, not the canonical provider
// type. The catalog is keyed by canonical provider type only, so the
// enrichment lookup must resolve the alias to canonical type first — this
// guards against that lookup regressing to keying on the alias directly,
// which would silently zero out enrichment for every aliased instance.
func TestModels_EnrichesAliasedCatalogTierInstance(t *testing.T) {
	// Catalog is keyed by canonical provider type only -- there is no
	// "ollama-cloud-a/model-a" entry, only "ollama-cloud/model-a". Served via
	// an httptest server and FERRO_MODEL_CATALOG_URL so New() loads it as the
	// gateway's real catalog (models.Catalog has no exported setter).
	catalog := models.Catalog{
		"ollama-cloud/model-a": {
			Provider:      "ollama-cloud",
			ModelID:       "model-a",
			Mode:          models.ModeChat,
			ContextWindow: 128000,
			Pricing: models.Pricing{
				InputPerMTokens:  ptrFloat64(1.5),
				OutputPerMTokens: ptrFloat64(3.0),
			},
		},
	}
	catalogBody, err := json.Marshal(catalog)
	if err != nil {
		t.Fatalf("marshal catalog: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(catalogBody)
	}))
	t.Cleanup(srv.Close)
	t.Setenv(models.CatalogURLEnv, srv.URL)

	gw, err := aigateway.New(aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: "ollama-cloud-a"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = gw.Close() })

	// Register as an aliased instance: alias "ollama-cloud-a", canonical type
	// "ollama-cloud". The provider itself declares no models via Models() so
	// AllModels() falls through to the catalog-precedence tier.
	if err := gw.RegisterProviderAs("ollama-cloud-a", "ollama-cloud", &nonProxyProvider{
		name:   "ollama-cloud",
		models: nil,
	}); err != nil {
		t.Fatalf("RegisterProviderAs: %v", err)
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/models", nil)
	w := httptest.NewRecorder()
	Models(gw)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	var resp struct {
		Data []EnrichedModelInfo `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	var found *EnrichedModelInfo
	for i := range resp.Data {
		if resp.Data[i].ID == "model-a" {
			found = &resp.Data[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected model-a in response, got %+v", resp.Data)
	}

	if found.OwnedBy != "ollama-cloud-a" {
		t.Errorf("OwnedBy = %q, want alias %q for operator distinguishability", found.OwnedBy, "ollama-cloud-a")
	}
	if found.ContextWindow == 0 {
		t.Error("expected non-zero ContextWindow from catalog enrichment, got 0 -- alias was not resolved to canonical type for the catalog lookup")
	}
}

func ptrFloat64(f float64) *float64 { return &f }
