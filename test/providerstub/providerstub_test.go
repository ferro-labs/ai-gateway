package providerstub

import (
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

// TestFixturesCoverAllProviders keeps the shared stub table honest as providers
// are added: every registered provider must be in exactly one of Fixtures or
// Uncovered, and both may only name real providers. It runs under plain
// `go test`, the same guard shape conformance uses — so the bench and fuzz
// suites that build on this table cannot silently drop a provider.
func TestFixturesCoverAllProviders(t *testing.T) {
	fx, unc := Fixtures(), Uncovered()
	for _, entry := range providers.AllProviders() {
		_, hasFixture := fx[entry.ID]
		_, isUncovered := unc[entry.ID]
		switch {
		case hasFixture && isUncovered:
			t.Errorf("provider %q is in both Fixtures and Uncovered; remove one", entry.ID)
		case !hasFixture && !isUncovered:
			t.Errorf("provider %q is in neither Fixtures nor Uncovered; add it to one", entry.ID)
		}
	}
	for id := range fx {
		if _, ok := providers.GetProviderEntry(id); !ok {
			t.Errorf("Fixtures names unregistered provider %q", id)
		}
	}
	for id, reason := range unc {
		if _, ok := providers.GetProviderEntry(id); !ok {
			t.Errorf("Uncovered names unregistered provider %q", id)
		}
		if strings.TrimSpace(reason) == "" {
			t.Errorf("Uncovered[%q] has no reason", id)
		}
	}
}

// TestBuildAllProviders proves every fixture builds and its stub answers, so a
// broken fixture is caught here rather than only inside a benchmark or fuzz run.
func TestBuildAllProviders(t *testing.T) {
	for id, fx := range Fixtures() {
		t.Run(id, func(t *testing.T) {
			restore := InstallResponse(id, 200, LoadChatResponse(t, id))
			defer restore()
			p := Build(t, id)
			if !p.SupportsModel(fx.Model) {
				t.Fatalf("%s does not support fixture model %q", id, fx.Model)
			}
			if _, err := p.Complete(t.Context(), ChatRequest(fx.Model)); err != nil {
				t.Fatalf("%s Complete against its own testdata: %v", id, err)
			}
		})
	}
}
