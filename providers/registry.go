package providers

import "fmt"

// Registry manages a collection of providers for lookup by name.
type Registry struct {
	providers map[string]Provider
}

// NewRegistry creates a new empty provider registry.
func NewRegistry() *Registry {
	return &Registry{
		providers: make(map[string]Provider),
	}
}

// Register adds a provider to the registry.
func (r *Registry) Register(p Provider) {
	r.providers[p.Name()] = p
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
