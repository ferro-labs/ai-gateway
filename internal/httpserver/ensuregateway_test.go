package httpserver

import (
	"context"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

// ensureGatewayStubProvider is a minimal provider stub used to verify that
// ensureGateway's fallback-gateway construction preserves routing aliases.
// Both instances in TestEnsureGateway_PreservesAliasesForSameNameInstances
// report the same Name() (mirroring two same-type provider instances, e.g.
// two Ollama Cloud accounts under distinct aliases) but carry distinguishable
// model lists so the test can tell them apart after ensureGateway runs.
type ensureGatewayStubProvider struct {
	name   string
	models []providers.ModelInfo
}

func (p ensureGatewayStubProvider) Name() string              { return p.name }
func (p ensureGatewayStubProvider) SupportedModels() []string { return []string{p.models[0].ID} }
func (p ensureGatewayStubProvider) SupportsModel(m string) bool {
	return m == p.models[0].ID
}
func (p ensureGatewayStubProvider) Models() []providers.ModelInfo { return p.models }
func (p ensureGatewayStubProvider) Complete(_ context.Context, _ providers.Request) (*providers.Response, error) {
	return &providers.Response{ID: "stub-1", Model: p.models[0].ID}, nil
}

// TestEnsureGateway_PreservesAliasesForSameNameInstances guards against the
// bug where ensureGateway's fallback-gateway construction called
// created.RegisterProvider(p), which re-derives the registration key from
// p.Name() and discards the registry's alias key entirely. Two aliased
// instances of the same provider type (same Name(), different alias) would
// collide and the second registration would silently clobber the first.
// ensureGateway must instead call RegisterProviderAs(alias, canonical, p),
// mirroring internal/bootstrap.BuildGateway.
func TestEnsureGateway_PreservesAliasesForSameNameInstances(t *testing.T) {
	instanceA := ensureGatewayStubProvider{
		name:   "ollama-cloud",
		models: []providers.ModelInfo{{ID: "model-a", Object: "model", OwnedBy: "ollama-cloud-a"}},
	}
	instanceB := ensureGatewayStubProvider{
		name:   "ollama-cloud",
		models: []providers.ModelInfo{{ID: "model-b", Object: "model", OwnedBy: "ollama-cloud-b"}},
	}

	reg := providers.NewRegistry()
	reg.RegisterAs("ollama-cloud-a", "ollama-cloud", instanceA)
	reg.RegisterAs("ollama-cloud-b", "ollama-cloud", instanceB)

	gw := ensureGateway(nil, reg)
	if gw == nil {
		t.Fatal("ensureGateway returned nil")
	}

	pa, ok := gw.GetProvider("ollama-cloud-a")
	if !ok {
		t.Fatal("expected alias ollama-cloud-a to resolve to a provider")
	}
	pb, ok := gw.GetProvider("ollama-cloud-b")
	if !ok {
		t.Fatal("expected alias ollama-cloud-b to resolve to a provider")
	}

	spA, ok := pa.(ensureGatewayStubProvider)
	if !ok || spA.models[0].ID != "model-a" {
		t.Errorf("ollama-cloud-a resolved to wrong provider instance: %+v", pa)
	}
	spB, ok := pb.(ensureGatewayStubProvider)
	if !ok || spB.models[0].ID != "model-b" {
		t.Errorf("ollama-cloud-b resolved to wrong provider instance (likely clobbered by the other alias): %+v", pb)
	}

	if got := gw.CanonicalProviderType("ollama-cloud-a"); got != "ollama-cloud" {
		t.Errorf("CanonicalProviderType(ollama-cloud-a) = %q, want %q", got, "ollama-cloud")
	}
	if got := gw.CanonicalProviderType("ollama-cloud-b"); got != "ollama-cloud" {
		t.Errorf("CanonicalProviderType(ollama-cloud-b) = %q, want %q", got, "ollama-cloud")
	}
}
