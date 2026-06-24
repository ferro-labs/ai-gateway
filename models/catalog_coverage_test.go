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

		provider, err := entry.Build(catalogCoverageConfig(entry))
		if err != nil {
			t.Fatalf("build provider %s: %v", entry.ID, err)
		}

		models := provider.SupportedModels()
		if len(models) == 0 {
			t.Fatalf("provider %s returned no supported models", entry.ID)
		}
		samples := catalogCoverageSampleModels(provider.Name(), models)
		for _, sampleModel := range samples {
			if !provider.SupportsModel(sampleModel) {
				t.Fatalf("provider %s does not support catalog coverage sample %q", provider.Name(), sampleModel)
			}
			if _, ok := catalog.Get(provider.Name() + "/" + sampleModel); !ok {
				t.Fatalf("catalog_backup.json does not resolve provider sample %s/%s", provider.Name(), sampleModel)
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

func catalogCoverageConfig(entry providers.ProviderEntry) providers.ProviderConfig {
	cfg := make(providers.ProviderConfig, len(entry.EnvMappings)+4)
	for _, mapping := range entry.EnvMappings {
		cfg[mapping.ConfigKey] = catalogCoverageValue(mapping.ConfigKey)
	}

	// Bedrock uses an OR-style configured gate, so it has no required env var.
	// Static dummy credentials keep construction local and deterministic.
	if entry.ID == providers.NameBedrock {
		cfg[providers.CfgKeyRegion] = "us-east-1"
		cfg[providers.CfgKeyAccessKeyID] = "test-access-key"
		cfg[providers.CfgKeySecretAccessKey] = "test-secret-key"
	}

	return cfg
}

func catalogCoverageValue(configKey string) string {
	switch configKey {
	case providers.CfgKeyBaseURL:
		return "https://example.com"
	case providers.CfgKeyAPIVersion:
		return "2024-10-21"
	case providers.CfgKeyAccountID:
		return "test-account"
	case providers.CfgKeyDeployment:
		return "gpt-4o"
	case providers.CfgKeyProjectID:
		return "test-project"
	case providers.CfgKeyRegion:
		return "us-east-1"
	case providers.CfgKeyServiceAccountJSON:
		return ""
	case providers.CfgKeyAccessKeyID:
		return "test-access-key"
	case providers.CfgKeySecretAccessKey:
		return "test-secret-key"
	case providers.CfgKeySessionToken:
		return "test-session-token"
	case providers.CfgKeyHost:
		return "http://localhost:11434"
	case providers.CfgKeyModels:
		return ""
	case providers.CfgKeyAPIToken:
		return "test-api-token"
	case providers.CfgKeyTextModels:
		return ""
	case providers.CfgKeyImageModels:
		return ""
	default:
		return "test-api-key"
	}
}

func catalogCoverageExcluded(providerID string) bool {
	switch providerID {
	case providers.NameHuggingFace, providers.NameNVIDIANIM, providers.NameQwen:
		// These provider packages exist, but the embedded catalog currently has no
		// matching top-level provider prefix for their direct API provider names.
		return true
	case providers.NameOllama, providers.NameOllamaCloud, providers.NameReplicate:
		// These providers primarily route user-configured/local model IDs, so a
		// single embedded public-catalog sample is not stable enough for this gate.
		return true
	default:
		return false
	}
}
