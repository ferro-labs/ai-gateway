package aigateway

import "github.com/ferro-labs/ai-gateway/providers"

// modelLookupIndex holds the exact model→provider-name maps used for O(1)
// routing lookups. Each map is keyed by model ID and rebuilt under g.mu whenever
// the provider set or catalog changes (see rebuildModelIndexesLocked).
type modelLookupIndex struct {
	exactProviders       map[string][]string
	exactStreamProviders map[string][]string
	exactEmbedProviders  map[string][]string
	exactImageProviders  map[string][]string
}

// modelsForRoutingLocked returns the routable model IDs for a provider: its
// hardcoded SupportedModels merged (de-duplicated, hardcoded-first) with any
// models the catalog advertises for it, plus any live-discovered models.
// Caller must hold g.mu.
func (g *Gateway) modelsForRoutingLocked(name string, p providers.Provider) []string {
	hardcoded := p.SupportedModels()
	catModels := g.catalog.ModelsForProvider(name)
	discovered := g.discoveredModels[name]
	if len(catModels) == 0 && len(discovered) == 0 {
		return hardcoded
	}
	seen := make(map[string]struct{}, len(hardcoded)+len(catModels)+len(discovered))
	out := make([]string, 0, len(hardcoded)+len(catModels)+len(discovered))
	for _, m := range hardcoded {
		if _, ok := seen[m]; !ok {
			seen[m] = struct{}{}
			out = append(out, m)
		}
	}
	for _, m := range catModels {
		if _, ok := seen[m]; !ok {
			seen[m] = struct{}{}
			out = append(out, m)
		}
	}
	for _, m := range discovered {
		if _, ok := seen[m.ID]; !ok {
			seen[m.ID] = struct{}{}
			out = append(out, m.ID)
		}
	}
	return out
}

// rebuildModelIndexesLocked repopulates every exact model→provider index from
// the current provider set. Caller must hold g.mu (write).
func (g *Gateway) rebuildModelIndexesLocked() {
	g.modelIndex.exactProviders = make(map[string][]string)
	g.modelIndex.exactStreamProviders = make(map[string][]string)
	g.modelIndex.exactEmbedProviders = make(map[string][]string)
	g.modelIndex.exactImageProviders = make(map[string][]string)

	for _, name := range g.providerNames {
		p, ok := g.providers[name]
		if !ok {
			continue
		}
		models := g.modelsForRoutingLocked(name, p)
		for _, model := range models {
			g.modelIndex.exactProviders[model] = append(g.modelIndex.exactProviders[model], name)
		}
		indexModelsIfImplements[providers.StreamProvider](p, name, models, g.modelIndex.exactStreamProviders)
		indexModelsIfImplements[providers.EmbeddingProvider](p, name, models, g.modelIndex.exactEmbedProviders)
		indexModelsIfImplements[providers.ImageProvider](p, name, models, g.modelIndex.exactImageProviders)
	}
}

// indexModelsIfImplements appends name under each of models in index, but only
// when p implements the capability interface T. Used to populate the per-capability
// exact indexes without repeating the type-assert-and-append block per capability.
func indexModelsIfImplements[T any](p providers.Provider, name string, models []string, index map[string][]string) {
	if _, ok := any(p).(T); !ok {
		return
	}
	for _, model := range models {
		index[model] = append(index[model], name)
	}
}

// findByModelLocked resolves model to a provider implementing capability T. It
// consults the exact-match index first (returning the first registered provider
// for that model), then falls back to a linear scan of providerNames for any
// provider that SupportsModel(model) and implements T. Caller must hold g.mu.
func findByModelLocked[T any](g *Gateway, index map[string][]string, model string) (name string, impl T, ok bool) {
	if exact := index[model]; len(exact) > 0 {
		if t, is := any(g.providers[exact[0]]).(T); is {
			return exact[0], t, true
		}
	}
	for _, n := range g.providerNames {
		p, exists := g.providers[n]
		if !exists || !p.SupportsModel(model) {
			continue
		}
		if t, is := any(p).(T); is {
			return n, t, true
		}
	}
	var zero T
	return "", zero, false
}

func (g *Gateway) findProviderByModelLocked(model string) (providers.Provider, bool) {
	_, p, ok := findByModelLocked[providers.Provider](g, g.modelIndex.exactProviders, model)
	return p, ok
}

func (g *Gateway) findStreamingProviderMatchByModelLocked(model string) (string, providers.StreamProvider, bool) {
	return findByModelLocked[providers.StreamProvider](g, g.modelIndex.exactStreamProviders, model)
}

func (g *Gateway) findStreamingProviderByModelLocked(model string) (providers.StreamProvider, bool) {
	_, sp, ok := g.findStreamingProviderMatchByModelLocked(model)
	return sp, ok
}

func (g *Gateway) findEmbeddingProviderByModelLocked(model string) (providers.EmbeddingProvider, bool) {
	_, ep, ok := findByModelLocked[providers.EmbeddingProvider](g, g.modelIndex.exactEmbedProviders, model)
	return ep, ok
}

func (g *Gateway) findImageProviderByModelLocked(model string) (providers.ImageProvider, bool) {
	_, ip, ok := findByModelLocked[providers.ImageProvider](g, g.modelIndex.exactImageProviders, model)
	return ip, ok
}
