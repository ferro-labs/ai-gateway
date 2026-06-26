package bootstrap

import (
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

func TestRegisterProvidersRegistersBedrockWithBearerTokenOnly(t *testing.T) {
	for _, entry := range providers.AllProviders() {
		for _, mapping := range entry.EnvMappings {
			t.Setenv(mapping.EnvVar, "")
		}
	}
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "test-bearer-token")

	registry := RegisterProviders()
	p, ok := registry.Get(providers.NameBedrock)
	if !ok {
		t.Fatal("Bedrock provider was not registered")
	}

	proxiable, ok := p.(interface {
		AuthHeaders() map[string]string
	})
	if !ok {
		t.Fatalf("Bedrock provider type %T does not expose AuthHeaders", p)
	}
	if got := proxiable.AuthHeaders()["Authorization"]; got != "Bearer test-bearer-token" {
		t.Errorf("Authorization = %q, want Bearer test-bearer-token", got)
	}
}
