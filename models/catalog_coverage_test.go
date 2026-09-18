package models

import (
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

func TestCatalogBackupCoversRegisteredProviders(t *testing.T) {
	catalog, err := parse(bundledCatalog)
	if err != nil {
		t.Fatalf("parse bundled catalog: %v", err)
	}

	for _, entry := range providers.AllProviders() {
		if catalogCoverageExcluded(entry.ID) {
			continue
		}

		// The catalog is the inventory now, so coverage is asserted against it
		// directly rather than against a list the provider used to hardcode.
		// This fails when the catalog stops carrying a registered provider —
		// the drift that would silently empty /v1/models for it.
		models := catalog.ModelsForProvider(entry.ID)
		if len(models) == 0 {
			t.Fatalf("catalog carries no models for registered provider %s", entry.ID)
		}

		// Every advertised id must resolve back for metadata, or /v1/models
		// serves a row with no context window, mode, or pricing behind it.
		//
		// SupportsModel is deliberately not asserted here. It answers whether
		// the provider will accept an id, which is not the same question as
		// whether the catalog lists one: bedrock's rows include provisioned
		// -throughput pricing keys ("*/1-month-commitment/...") that are real
		// catalog entries and not callable model names. Holding the two to each
		// other fails on catalog shape, not on gateway behaviour.
		for _, sampleModel := range catalogCoverageSampleModels(entry.ID, models) {
			if _, ok := catalog.Get(entry.ID + "/" + sampleModel); !ok {
				t.Fatalf("catalog_backup.json does not resolve provider sample %s/%s", entry.ID, sampleModel)
			}
		}
	}
}

func catalogCoverageSampleModels(providerName string, models []string) []string {
	if _, ok := catalogProviderAliases[providerName]; ok {
		return models
	}
	return []string{catalogCoverageSampleModel(providerName, models)}
}

func catalogCoverageSampleModel(providerName string, models []string) string {
	switch providerName {
	case providers.NameCloudflare:
		return "@cf/meta/llama-2-7b-chat-fp16"
	default:
		return models[0]
	}
}

func catalogCoverageExcluded(providerID string) bool {
	switch providerID {
	case providers.NameOllama, providers.NameOllamaCloud, providers.NameReplicate:
		// These providers primarily route user-configured/local model IDs, so a
		// single embedded public-catalog sample is not stable enough for this gate.
		return true
	case providers.NameRequesty:
		// Requesty is an OpenAI-compatible gateway that routes user-configured
		// provider/model IDs; its model list is not part of the embedded public
		// catalog, so a single sample is not stable enough for this gate.
		return true
	default:
		return false
	}
}
