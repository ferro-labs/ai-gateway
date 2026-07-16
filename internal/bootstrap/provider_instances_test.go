package bootstrap

import (
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/providers"
)

func TestRegisterProviderInstancesRegistersAliasUnderCanonicalType(t *testing.T) {
	t.Setenv("OLLAMA_CLOUD_INSTANCE_KEY", "test-key")

	cfg := &aigateway.Config{
		ProviderInstances: []aigateway.ProviderInstanceConfig{
			{
				Alias: "ollama-cloud-a",
				Type:  providers.NameOllamaCloud,
				Credentials: map[string]string{
					providers.CfgKeyAPIKey: "${OLLAMA_CLOUD_INSTANCE_KEY}",
				},
			},
		},
	}

	registry := providers.NewRegistry()
	RegisterProviderInstances(registry, cfg)

	if _, ok := registry.Get("ollama-cloud-a"); !ok {
		t.Fatal("alias ollama-cloud-a should be registered")
	}
	canonical, ok := registry.CanonicalType("ollama-cloud-a")
	if !ok {
		t.Fatal("canonical type should be tracked for the alias")
	}
	if canonical != providers.NameOllamaCloud {
		t.Fatalf("canonical type = %q, want %q", canonical, providers.NameOllamaCloud)
	}
}

func TestRegisterProviderInstancesSkipsUnknownType(t *testing.T) {
	before := initFailureCount(t, "not-a-real-provider")

	cfg := &aigateway.Config{
		ProviderInstances: []aigateway.ProviderInstanceConfig{
			{
				Alias: "bogus-a",
				Type:  "not-a-real-provider",
			},
		},
	}

	registry := providers.NewRegistry()
	RegisterProviderInstances(registry, cfg)

	if _, ok := registry.Get("bogus-a"); ok {
		t.Fatal("alias with unknown provider type should not be registered")
	}
	if delta := initFailureCount(t, "not-a-real-provider") - before; delta != 1 {
		t.Fatalf("init failure counter delta = %v, want 1", delta)
	}
}

func TestRegisterProviderInstancesResolvesEnvVarCredential(t *testing.T) {
	t.Setenv("OLLAMA_CLOUD_RESOLVE_KEY", "resolved-value")

	cfg := &aigateway.Config{
		ProviderInstances: []aigateway.ProviderInstanceConfig{
			{
				Alias: "ollama-cloud-resolved",
				Type:  providers.NameOllamaCloud,
				Credentials: map[string]string{
					providers.CfgKeyAPIKey: "${OLLAMA_CLOUD_RESOLVE_KEY}",
				},
			},
		},
	}

	registry := providers.NewRegistry()
	RegisterProviderInstances(registry, cfg)

	if _, ok := registry.Get("ollama-cloud-resolved"); !ok {
		t.Fatal("alias should be registered once the env var resolves")
	}
}

func TestRegisterProviderInstancesSkipsUnresolvableEnvVar(t *testing.T) {
	before := initFailureCount(t, providers.NameOllamaCloud)

	cfg := &aigateway.Config{
		ProviderInstances: []aigateway.ProviderInstanceConfig{
			{
				Alias: "ollama-cloud-unresolved",
				Type:  providers.NameOllamaCloud,
				Credentials: map[string]string{
					providers.CfgKeyAPIKey: "${OLLAMA_CLOUD_DEFINITELY_UNSET_VAR}",
				},
			},
		},
	}

	registry := providers.NewRegistry()
	RegisterProviderInstances(registry, cfg)

	if _, ok := registry.Get("ollama-cloud-unresolved"); ok {
		t.Fatal("alias with unresolvable ${VAR} credential should not be registered")
	}
	if delta := initFailureCount(t, providers.NameOllamaCloud) - before; delta != 1 {
		t.Fatalf("init failure counter delta = %v, want 1", delta)
	}
}

func TestRegisterProviderInstancesNilConfigIsNoop(t *testing.T) {
	registry := providers.NewRegistry()
	RegisterProviderInstances(registry, nil)

	if len(registry.List()) != 0 {
		t.Fatalf("expected no providers registered, got %v", registry.List())
	}
}

// TestRegisterProviderInstancesSkipsAliasCollidingWithCanonicalProvider is
// defense-in-depth coverage: ValidateConfig already rejects an instance Alias
// that matches a canonical providers.ProviderEntry.ID (e.g. "openai") before
// RegisterProviderInstances ever runs, but this test bypasses that validation
// entirely -- calling RegisterProviderInstances directly with a colliding
// alias -- to prove the function itself also refuses to silently overwrite a
// canonical provider's registry entry, in case that upstream guarantee is
// ever weakened or bypassed by a future call site.
func TestRegisterProviderInstancesSkipsAliasCollidingWithCanonicalProvider(t *testing.T) {
	t.Setenv("OPENAI_INSTANCE_KEY", "test-key")
	// The failure metric this path increments is labeled by inst.Type
	// ("ollama-cloud"), not by the colliding alias ("openai").
	before := initFailureCount(t, providers.NameOllamaCloud)

	cfg := &aigateway.Config{
		ProviderInstances: []aigateway.ProviderInstanceConfig{
			{
				// "openai" is a canonical provider ID (providers.NameOpenAI),
				// so this alias must be rejected even though it names a
				// different provider Type.
				Alias: providers.NameOpenAI,
				Type:  providers.NameOllamaCloud,
				Credentials: map[string]string{
					providers.CfgKeyAPIKey: "${OPENAI_INSTANCE_KEY}",
				},
			},
		},
	}

	registry := providers.NewRegistry()
	RegisterProviderInstances(registry, cfg)

	if _, ok := registry.Get(providers.NameOpenAI); ok {
		t.Fatal("alias colliding with a canonical provider name should not be registered")
	}
	if delta := initFailureCount(t, providers.NameOllamaCloud) - before; delta != 1 {
		t.Fatalf("init failure counter delta = %v, want 1", delta)
	}
}
