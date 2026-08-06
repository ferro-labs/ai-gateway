package handler

import (
	"encoding/json"
	"testing"

	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// The dashboard's Playground picker drops every model this endpoint reports as
// deprecated, so `deprecated` is the whole basis on which a retired model stops
// being offered. These cases pin that it is derived from the catalog's
// lifecycle status rather than defaulted, and that the endpoint keeps listing
// the model — /v1/models is OpenAI-compatible, and a consumer that wants the
// full inventory has to be able to get it and judge it by this flag.
func TestEnrichFromCatalogReportsLifecycle(t *testing.T) {
	catalog := models.Catalog{
		"openai/gpt-4o": {
			Provider:  "openai",
			ModelID:   "gpt-4o",
			Mode:      models.ModeChat,
			Lifecycle: models.Lifecycle{Status: "ga"},
		},
		"openai/gpt-3.5-turbo": {
			Provider:  "openai",
			ModelID:   "gpt-3.5-turbo",
			Mode:      models.ModeChat,
			Lifecycle: models.Lifecycle{Status: "deprecated"},
		},
		"openai/text-davinci-003": {
			Provider:  "openai",
			ModelID:   "text-davinci-003",
			Mode:      models.ModeChat,
			Lifecycle: models.Lifecycle{Status: "legacy"},
		},
	}
	models.BuildIndex(catalog)

	tests := []struct {
		name           string
		modelID        string
		wantStatus     string
		wantDeprecated bool
	}{
		{name: "ga model is offered", modelID: "gpt-4o", wantStatus: "ga"},
		{name: "deprecated status sets the flag", modelID: "gpt-3.5-turbo", wantStatus: "deprecated", wantDeprecated: true},
		{name: "legacy status sets the flag too", modelID: "text-davinci-003", wantStatus: "legacy", wantDeprecated: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := enrichFromCatalog(catalog, core.ModelInfo{ID: tt.modelID, OwnedBy: "openai"})

			if got.ID != tt.modelID {
				t.Errorf("ID = %q, want %q", got.ID, tt.modelID)
			}
			if got.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, tt.wantStatus)
			}
			if got.Deprecated != tt.wantDeprecated {
				t.Errorf("Deprecated = %v, want %v", got.Deprecated, tt.wantDeprecated)
			}
		})
	}
}

// A model the catalog has no entry for is unknown, not retired. Reporting it as
// deprecated would hide it from the picker on no evidence at all — which is
// exactly what a self-hosted or newly released model looks like here.
func TestEnrichFromCatalogLeavesUnknownModelUnflagged(t *testing.T) {
	catalog := models.Catalog{}
	models.BuildIndex(catalog)

	got := enrichFromCatalog(catalog, core.ModelInfo{ID: "house-tuned-llama", OwnedBy: "ollama"})

	if got.Deprecated {
		t.Error("Deprecated = true for a model with no catalog entry, want false")
	}
	if got.Status != "" {
		t.Errorf("Status = %q for a model with no catalog entry, want empty", got.Status)
	}
	if got.OwnedBy != "ollama" {
		t.Errorf("OwnedBy = %q, want %q", got.OwnedBy, "ollama")
	}
}

// created is part of the OpenAI Model object and is typed as a plain integer by
// the official SDKs, so omitting it fails a strict client's deserialization of
// the entire list. It is reported when the provider's own model listing carries
// it, and written as zero — never omitted, and never invented — when it does not.
func TestModelsAlwaysReportsCreated(t *testing.T) {
	catalog := models.Catalog{}
	models.BuildIndex(catalog)

	t.Run("provider-reported time is preserved", func(t *testing.T) {
		got := enrichFromCatalog(catalog, core.ModelInfo{ID: "m", OwnedBy: "openai", Created: 1_700_000_000})
		if got.Created != 1_700_000_000 {
			t.Errorf("Created = %d, want 1700000000", got.Created)
		}
	})

	t.Run("the key is written even when unreported", func(t *testing.T) {
		b, err := json.Marshal(enrichFromCatalog(catalog, core.ModelInfo{ID: "m", OwnedBy: "openai"}))
		if err != nil {
			t.Fatalf("Marshal(): %v", err)
		}
		var got map[string]any
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("Unmarshal(): %v", err)
		}
		if _, ok := got["created"]; !ok {
			t.Errorf("created absent from %s", b)
		}
	})
}
