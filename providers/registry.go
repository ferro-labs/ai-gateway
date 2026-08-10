package providers

import "github.com/ferro-labs/ai-gateway/providers/core"

// Registry manages a collection of providers for lookup by name.
type Registry struct {
	providers map[string]Provider
	order     []string
}

// NewRegistry creates a new empty provider registry.
func NewRegistry() *Registry {
	return &Registry{
		providers: make(map[string]Provider),
	}
}

// Register adds a provider to the registry.
func (r *Registry) Register(p Provider) {
	if _, exists := r.providers[p.Name()]; !exists {
		r.order = append(r.order, p.Name())
	}
	r.providers[p.Name()] = p
}

// Get returns a provider by name and whether it was found.
func (r *Registry) Get(name string) (Provider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

// List returns the names of all registered providers.
func (r *Registry) List() []string {
	names := make([]string, len(r.order))
	copy(names, r.order)
	return names
}

// ModelsFor reports the models the named provider was configured with.
//
// A bare Registry holds no catalog and runs no discovery, so this is only the
// config-derived set — an Azure deployment name, an OLLAMA_MODELS entry — and
// is empty for the providers whose inventory lives in the catalog. The Gateway
// composes all three sources; a Registry can only answer for the one it has.
func (r *Registry) ModelsFor(name string) []ModelInfo {
	p, ok := r.providers[name]
	if !ok {
		return nil
	}
	cm, ok := As[ConfiguredModelProvider](p)
	if !ok {
		return nil
	}
	return core.ModelsFromList(name, cm.ConfiguredModels())
}

// AllModels returns the configured models of every registered provider. See
// ModelsFor for why a Registry's answer is narrower than the Gateway's.
func (r *Registry) AllModels() []ModelInfo {
	models := make([]ModelInfo, 0, len(r.order))
	for _, name := range r.order {
		models = append(models, r.ModelsFor(name)...)
	}
	return models
}

// FindByModel returns the first registered provider whose advisory
// SupportsModel accepts the given model. Registration order is retained
// explicitly so overlapping/wildcard providers resolve deterministically rather
// than following Go map iteration.
//
// This is a claim, not ownership. Several providers answer SupportsModel
// `return true` because their upstream's inventory is not theirs to enumerate,
// so an unrecognised name resolves here to whichever of them registers first —
// and so does a name another provider genuinely owns. Only the Gateway can
// decide ownership: it holds the routing index built from the catalog, live
// discovery and the models this instance's config named, and its own
// FindByModel answers from that. A bare Registry has none of those, which is
// why this method is left as it is rather than redefined to something the type
// cannot compute.
//
// Callers deciding where to send a request body should hold a ProviderSource
// and be given the Gateway. This remains for callers that want exactly what it
// says: any provider that claims the name.
func (r *Registry) FindByModel(model string) (Provider, bool) {
	for _, name := range r.order {
		p := r.providers[name]
		if p.SupportsModel(model) {
			return p, true
		}
	}
	return nil, false
}
