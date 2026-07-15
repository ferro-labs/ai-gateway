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
	brokenBefore := initFailureCount(t, "broken-provider")
	goodBefore := initFailureCount(t, "good-provider")

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
	if delta := initFailureCount(t, "broken-provider") - brokenBefore; delta != 1 {
		t.Fatalf("provider init failure counter delta = %v, want 1", delta)
	}
	if delta := initFailureCount(t, "good-provider") - goodBefore; delta != 0 {
		t.Fatalf("good provider init failure counter delta = %v, want 0", delta)
	}
}

// Bedrock is registered by its own dual-key branch rather than the generic loop,
// so it needs the same failure signal: an access key without a secret builds no
// provider, the gateway still starts, and /health still reports it as serving.
func TestRegisterBedrockProviderCountsInitFailure(t *testing.T) {
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAEXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "") // static credentials require both
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "")

	before := initFailureCount(t, providers.NameBedrock)

	registry := providers.NewRegistry()
	registerBedrockProvider(registry)

	if _, ok := registry.Get(providers.NameBedrock); ok {
		t.Fatal("bedrock should not register with incomplete static credentials")
	}
	if delta := initFailureCount(t, providers.NameBedrock) - before; delta != 1 {
		t.Fatalf("bedrock init failure counter delta = %v, want 1", delta)
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
