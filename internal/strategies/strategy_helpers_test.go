package strategies

import (
	"context"

	"github.com/ferro-labs/ai-gateway/providers"
)

type mockProvider struct {
	name   string
	models []string
	resp   *providers.Response
	err    error
	calls  int
}

func (m *mockProvider) Name() string                  { return m.name }
func (m *mockProvider) SupportedModels() []string     { return m.models }
func (m *mockProvider) Models() []providers.ModelInfo { return nil }
func (m *mockProvider) SupportsModel(model string) bool {
	for _, mm := range m.models {
		if mm == model {
			return true
		}
	}
	return false
}
func (m *mockProvider) Complete(_ context.Context, _ providers.Request) (*providers.Response, error) {
	m.calls++
	return m.resp, m.err
}

func newLookup(pp ...providers.Provider) ProviderLookup {
	m := make(map[string]providers.Provider)
	for _, p := range pp {
		m[p.Name()] = p
	}
	return func(name string) (providers.Provider, bool) {
		p, ok := m[name]
		return p, ok
	}
}

func lookupMissingAfterFirstHit(p providers.Provider) ProviderLookup {
	calls := 0
	return func(name string) (providers.Provider, bool) {
		if name != p.Name() {
			return nil, false
		}
		calls++
		if calls == 1 {
			return p, true
		}
		return nil, false
	}
}
