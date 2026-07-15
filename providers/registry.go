package providers

import "fmt"

// Registry manages a collection of providers for lookup by name.
type Registry struct {
	providers map[string]Provider
	// canonical maps a routing alias to the canonical provider type it was
	// registered under (i.e. ProviderEntry.ID / Provider.Name()). Populated
	// by RegisterAs so capability/cost-routing code can resolve an aliased
	// instance back to its real provider type.
	canonical map[string]string
}

// NewRegistry creates a new empty provider registry.
func NewRegistry() *Registry {
	return &Registry{
		providers: make(map[string]Provider),
		canonical: make(map[string]string),
	}
}

// Register adds a provider to the registry, keyed by its own reported name.
func (r *Registry) Register(p Provider) {
	r.RegisterAs(p.Name(), p.Name(), p)
}

// RegisterAs registers p under the given routing alias, recording
// canonicalType (the provider's real type, e.g. "ollama-cloud") for later
// resolution by capability/cost-routing code that needs the true provider
// type rather than the routing alias. alias may equal canonicalType (the
// common case — most providers have exactly one instance, registered under
// their own name). This allows two providers that report the same Name() to
// be registered independently under distinct aliases without clobbering
// each other.
func (r *Registry) RegisterAs(alias, canonicalType string, p Provider) {
	if r.canonical == nil {
		r.canonical = make(map[string]string)
	}
	r.providers[alias] = p
	r.canonical[alias] = canonicalType
}

// CanonicalType returns the real provider type an alias was registered
// under, and whether the alias is known at all. If the alias was never
// registered via RegisterAs/Register, ok is false and alias is returned
// unchanged so callers can decide the fallback themselves.
func (r *Registry) CanonicalType(alias string) (string, bool) {
	canonicalType, ok := r.canonical[alias]
	if !ok {
		return alias, false
	}
	return canonicalType, true
}

// Get returns a provider by name and whether it was found.
func (r *Registry) Get(name string) (Provider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

// MustGet returns a provider by name or panics if not found.
func (r *Registry) MustGet(name string) Provider {
	p, ok := r.providers[name]
	if !ok {
		panic(fmt.Sprintf("provider not found: %s", name))
	}
	return p
}

// List returns the names of all registered providers.
func (r *Registry) List() []string {
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	return names
}

// AllModels returns ModelInfo from all registered providers.
func (r *Registry) AllModels() []ModelInfo {
	var models []ModelInfo
	for _, p := range r.providers {
		models = append(models, p.Models()...)
	}
	return models
}

// FindByModel returns the first provider that supports the given model.
func (r *Registry) FindByModel(model string) (Provider, bool) {
	for _, p := range r.providers {
		if p.SupportsModel(model) {
			return p, true
		}
	}
	return nil, false
}
