package bootstrap

import (
	"context"
	"errors"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

func TestRegisterProviderEntriesSkipsBrokenProviderAndKeepsGoodProvider(t *testing.T) {
	t.Setenv("BROKEN_PROVIDER_KEY", "set")
	t.Setenv("GOOD_PROVIDER_KEY", "set")

	registry := providers.NewRegistry()
	registerProviderEntries(registry, []providers.ProviderEntry{
		{
			ID: "broken-provider",
			EnvMappings: []providers.EnvMapping{{
				ConfigKey: providers.CfgKeyAPIKey,
				EnvVar:    "BROKEN_PROVIDER_KEY",
				Required:  true,
			}},
			Build: func(providers.ProviderConfig) (providers.Provider, error) {
				return nil, errors.New("build failed")
			},
		},
		{
			ID: "good-provider",
			EnvMappings: []providers.EnvMapping{{
				ConfigKey: providers.CfgKeyAPIKey,
				EnvVar:    "GOOD_PROVIDER_KEY",
				Required:  true,
			}},
			Build: func(providers.ProviderConfig) (providers.Provider, error) {
				return bootstrapProvider{name: "good-provider", models: []string{"good-model"}}, nil
			},
		},
	})

	if _, ok := registry.Get("broken-provider"); ok {
		t.Fatal("broken provider should be skipped")
	}
	if _, ok := registry.Get("good-provider"); !ok {
		t.Fatal("good provider should be registered")
	}
}

type bootstrapProvider struct {
	name   string
	models []string
}

func (p bootstrapProvider) Name() string                  { return p.name }
func (p bootstrapProvider) SupportedModels() []string     { return p.models }
func (p bootstrapProvider) Models() []providers.ModelInfo { return nil }
func (p bootstrapProvider) SupportsModel(model string) bool {
	for _, m := range p.models {
		if m == model {
			return true
		}
	}
	return false
}
func (bootstrapProvider) Complete(context.Context, providers.Request) (*providers.Response, error) {
	return nil, nil
}
