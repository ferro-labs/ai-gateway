package bootstrap

import (
	"context"
	"errors"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/prometheus/client_golang/prometheus"
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

	// Skipping must not be silent: /health still answers 200 because it only
	// counts registered providers, so this counter is the operator's only signal.
	if got := initFailureCount(t, "broken-provider"); got != 1 {
		t.Fatalf("provider init failure counter = %v, want 1", got)
	}
	if got := initFailureCount(t, "good-provider"); got != 0 {
		t.Fatalf("good provider recorded %v init failures, want 0", got)
	}
}

// initFailureCount reads gateway_provider_init_failures_total for one provider.
func initFailureCount(t *testing.T, provider string) float64 {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "gateway_provider_init_failures_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, label := range m.GetLabel() {
				if label.GetName() == "provider" && label.GetValue() == provider {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
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
