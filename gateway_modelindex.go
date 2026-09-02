package aigateway

import (
	"context"
	"maps"
	"slices"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/providers"
)

// routingSnapshot is an immutable view of the provider set and everything
// derived from it, published as a single value.
//
// It exists because routing reads several of these together and they only mean
// anything as a set: a model index that admits a provider the map no longer
// holds resolves to nothing. Reading them under separate lock acquisitions —
// which the request path does, so it never holds a lock across a network call —
// leaves room for a rebuild to land in between and pair a new index with an old
// provider map. Loading one snapshot and using it throughout closes that
// window, and costs readers an atomic load instead of a mutex.
//
// Every field is written once, before the value is published, and never
// mutated afterwards. A rebuild constructs a new snapshot; it never edits the
// live one.
//
// Circuit breakers and per-target limiters are deliberately not here. They are
// ensured lazily under a write lock on the request path itself
// (ensureCircuitBreakersLocked), so a snapshot holding them would have to be
// republished mid-request — which is both the contention this avoids and a far
// larger change to breaker lifecycle. A caller needing those alongside a
// provider keeps g.mu instead; holding it excludes a rebuild, so reading the
// snapshot under the lock is equally consistent. The rule is that no caller
// mixes a lock-free snapshot read with a lock-held read of the same generation.
type routingSnapshot struct {
	providers      map[string]providers.Provider
	providerNames  []string
	catalogModels  map[string][]string
	declaredModels map[string][]string
	discovered     map[string][]providers.ModelInfo
	index          modelLookupIndex
}

// emptyRoutingSnapshot is what a gateway reads before any provider is
// registered, so callers never have to nil-check the pointer.
var emptyRoutingSnapshot = &routingSnapshot{
	providers:      map[string]providers.Provider{},
	catalogModels:  map[string][]string{},
	declaredModels: map[string][]string{},
	discovered:     map[string][]providers.ModelInfo{},
	index: modelLookupIndex{
		exactProviders:       map[string][]string{},
		exactStreamProviders: map[string][]string{},
		exactEmbedProviders:  map[string][]string{},
		exactImageProviders:  map[string][]string{},
	},
}

// routing returns the current snapshot without taking a lock. Callers that need
// more than one fact about routing must load it once and read from that value,
// rather than calling this repeatedly — the point is to see a single generation.
func (g *Gateway) routing() *routingSnapshot {
	if snap := g.routingSnap.Load(); snap != nil {
		return snap
	}
	return emptyRoutingSnapshot
}

// modelLookupIndex holds the exact model→provider-name maps used for O(1)
// routing lookups. Each map is keyed by model ID and rebuilt under g.mu whenever
// the provider set or catalog changes (see rebuildModelIndexesLocked).
type modelLookupIndex struct {
	exactProviders       map[string][]string
	exactStreamProviders map[string][]string
	exactEmbedProviders  map[string][]string
	exactImageProviders  map[string][]string
}

// configuredModelsOf returns the models a provider's own configuration named —
// an Azure deployment, an OLLAMA_MODELS entry — or nil for the providers whose
// inventory the catalog supplies. See core.ConfiguredModelProvider.
func configuredModelsOf(p providers.Provider) []string {
	cm, ok := providers.As[providers.ConfiguredModelProvider](p)
	if !ok {
		return nil
	}
	return cm.ConfiguredModels()
}

// declaredModelsByTarget collects targets[].models and model_map keys into a
// per-provider list.
//
// A target's virtual_key IS the provider name everywhere else in routing
// (targetedProviders, IsTargetedProvider, routeTargets), so the same identity
// holds here. Two targets naming one provider contribute to one list, and the
// result is de-duplicated once so no reader has to.
//
// A key naming a provider this process never registered stays in the map and is
// simply never read: the index is built by walking registered providers. That is
// the existing treatment of an unroutable target — startup warns, /readyz
// reports it — and it is the one that lets a config name a provider whose
// credential has not rolled out yet.
func declaredModelsByTarget(targets []config.Target) map[string][]string {
	out := make(map[string][]string, len(targets))
	for _, t := range targets {
		for _, m := range t.Models {
			if !slices.Contains(out[t.VirtualKey], m) {
				out[t.VirtualKey] = append(out[t.VirtualKey], m)
			}
		}
		for m := range t.ModelMap {
			if !slices.Contains(out[t.VirtualKey], m) {
				out[t.VirtualKey] = append(out[t.VirtualKey], m)
			}
		}
	}
	return out
}

// modelsForRoutingLocked returns the routable model IDs for a provider: the
// union, de-duplicated, of everything that can name a model for it — the
// operator's targets[].models, the models the provider's own config named, the
// catalog, and live discovery.
//
// It is a union rather than a precedence because every one of the four is a
// statement that some model IS servable, and none of them is a statement that
// the others are wrong. Advertising (modelsFor) is where a precedence belongs,
// because there the question is which source describes the inventory best.
//
// The declared and configured sources lead because they are the two that can be
// right when the automatic ones are silent: a private Azure deployment name
// appears in no catalog and enumerates from no /models endpoint, and an operator
// declaring a model has looked at the upstream that the catalog has not caught
// up with. Order is cosmetic for routing — every member is admitted — but it
// keeps the operator's own words at the head of the list every reader prints.
//
// Caller must hold g.mu.
func (g *Gateway) modelsForRoutingLocked(name string, p providers.Provider) []string {
	declared := g.declaredModels[name]
	configured := configuredModelsOf(p)
	catModels := g.catalogModels[name]
	discovered := g.discoveredModels[name]

	total := len(declared) + len(configured) + len(catModels) + len(discovered)
	if total == 0 {
		return nil
	}
	seen := make(map[string]struct{}, total)
	out := make([]string, 0, total)
	add := func(id string) {
		if _, dup := seen[id]; dup {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for _, m := range declared {
		add(m)
	}
	for _, m := range configured {
		add(m)
	}
	for _, m := range catModels {
		add(m)
	}
	for _, m := range discovered {
		add(m.ID)
	}
	return out
}

// rebuildModelIndexesLocked repopulates every exact model→provider index from
// the current provider set, and drops the built strategy: it selects targets
// against a snapshot of this index, so leaving it in place would keep routing
// on the superseded one while /v1/models and FindByModel already answer from
// the new one. Caller must hold g.mu (write).
func (g *Gateway) rebuildModelIndexesLocked() {
	g.strategies = nil

	// Cache the catalog's per-provider lists before anything reads them.
	// Catalog.ModelsForProvider walks all ~2,500 entries per call, so deriving
	// this once per rebuild rather than once per request is the difference
	// between 2.5ms and a map read on every /v1/models and every routing
	// lookup. Rebuild is the correct seam because it already fires on both
	// triggers that can invalidate it: a catalog swap and a provider-set change.
	//
	// The slices are shared, not copied — every reader below and in AllModels
	// treats them as read-only.
	g.catalogModels = make(map[string][]string, len(g.providerNames))
	for _, name := range g.providerNames {
		p, ok := g.providers[name]
		if !ok {
			continue
		}
		// Key the catalog lookup on the provider's canonical vendor identity, not
		// the (possibly aliased) routing key it was registered under — otherwise an
		// alias inherits none of its provider's catalog models and they vanish from
		// routing and /v1/models.
		if ids := g.catalog.ModelsForProvider(providers.CanonicalName(p)); len(ids) > 0 {
			g.catalogModels[name] = ids
		}
	}

	// targets[].models is derived from the live config the same way, and for the
	// same reason: it is cheap, it changes only on the events that already reach
	// this function, and deriving it here is what makes a config reload republish
	// routing rather than leave it answering from the superseded target list.
	g.declaredModels = declaredModelsByTarget(g.config.Targets)

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

	g.publishRoutingLocked()
}

// publishRoutingLocked replaces the snapshot readers see with one built from
// the state just rebuilt. It is the only writer of g.routingSnap, and every
// mutation of the provider set, the catalog cache, or discovered models reaches
// it through rebuildModelIndexesLocked.
//
// The maps are cloned because their originals stay mutable under g.mu: a
// snapshot that aliased them would change under readers that are, by design,
// holding no lock. Cloning ~30 provider entries on a rebuild that happens at
// registration, on a 24h catalog refresh, and on a discovery round is not a
// cost worth avoiding.
//
// Caller must hold g.mu (write).
func (g *Gateway) publishRoutingLocked() {
	g.routingSnap.Store(&routingSnapshot{
		providers:      maps.Clone(g.providers),
		providerNames:  slices.Clone(g.providerNames),
		catalogModels:  maps.Clone(g.catalogModels),
		declaredModels: maps.Clone(g.declaredModels),
		discovered:     maps.Clone(g.discoveredModels),
		index:          g.modelIndex,
	})
}

// indexModelsIfImplements appends name under each of models in index, but only
// when p implements the capability interface T. Used to populate the per-capability
// exact indexes without repeating the type-assert-and-append block per capability.
func indexModelsIfImplements[T any](p providers.Provider, name string, models []string, index map[string][]string) {
	if _, ok := providers.As[T](p); !ok {
		return
	}
	for _, model := range models {
		index[model] = append(index[model], name)
	}
}

// findByModel resolves model to a provider implementing capability T. It
// consults the exact-match index first, in registration order, and only when
// the index records no owner at all falls back to a scan of the providers that
// declared core.AnyModelProvider.
//
// The fallback used to scan on SupportsModel, which twelve providers answer
// `return true` — so every unowned model resolved to whichever of them was
// registered first, and the handlers' pre-flight admitted a model nothing
// serves. Owners are walked in full before the fallback so a declared wildcard
// can never outrank a provider the index says owns the name.
//
// Both halves read the same snapshot, so the index cannot name a provider the
// map has already lost. Takes no lock.
func findByModel[T any](s *routingSnapshot, index map[string][]string, model string) (name string, impl T, ok bool) {
	owners := index[model]
	for _, n := range owners {
		if t, is := providers.As[T](s.providers[n]); is {
			return n, t, true
		}
	}
	if len(owners) == 0 {
		for _, n := range s.providerNames {
			p, exists := s.providers[n]
			if !exists || !servesAnyModel(p) {
				continue
			}
			if t, is := providers.As[T](p); is {
				return n, t, true
			}
		}
	}
	var zero T
	return "", zero, false
}

// servesAnyModel reports whether a provider declared core.AnyModelProvider —
// that its upstream accepts model ids no index can enumerate. It is the ONLY
// way a target becomes a candidate for a model the index has no owner for.
func servesAnyModel(p providers.Provider) bool {
	_, ok := providers.As[providers.AnyModelProvider](p)
	return ok
}

// ownsModel reports whether name is one of the providers the index records for
// model, and whether the index records any owner at all.
//
// The two answers are returned together because they are one decision, and
// splitting them is what let a wildcard win over an owner: with an owner
// present the answer is exact membership, and only with NO owner does a
// declared AnyModelProvider get a look in. That ordering is LiteLLM's — an
// exact model_list entry beats a wildcard entry, patterns sorted most specific
// first — and it is what makes `targets: [databricks, openai]` still send
// gpt-4o-mini to openai.
func ownsModel(index map[string][]string, name, model string) (owns, claimed bool) {
	owners := index[model]
	return slices.Contains(owners, name), len(owners) > 0
}

// supportsModelForRoutingLocked reports whether a configured target is a
// candidate for a model.
//
// The routing index is the sole authority: the union of the models this
// instance's config named, the catalog, and live discovery — exactly the set
// /v1/models advertises. A provider's own SupportsModel is NOT consulted.
// Twelve providers answered it `return true`, so an unknown model name made
// candidates of all of them and sent the prompt body to three before returning
// 404 — data egress decided by declaration order, while the gateway's own index
// knew which provider owned the name. A provider whose upstream genuinely
// accepts unenumerable ids says so with core.AnyModelProvider, and is offered
// only the names no target owns.
//
// Every surface asks through here, so chat, streaming, embeddings and images
// cannot disagree about who serves a model.
//
// Caller must hold g.mu (a read lock is sufficient).
func (g *Gateway) supportsModelForRoutingLocked(name string, p providers.Provider, model string) bool {
	owns, claimed := ownsModel(g.modelIndex.exactProviders, name, model)
	if claimed {
		return owns
	}
	return servesAnyModel(p)
}

// indexedProvider presents a provider with the model set the gateway actually
// routes: the union of the models its config named, the catalog, and live
// discovery (modelsForRoutingLocked). The routing strategies select candidates
// by calling SupportsModel on what they are handed, so this view is how they
// are given the index's answer rather than the provider's own — without it a
// strategy would refuse a model /v1/models advertises, and admit one no target
// owns.
//
// Like cbProvider and limitedProvider it lives only in the routing layer and is
// never stored in the registry, so capability detection elsewhere — model
// indexing, discovery, proxy eligibility — still sees the real provider.
type indexedProvider struct {
	providers.Provider
	name     string
	index    map[string][]string
	anyModel bool
}

// UnwrapIdentity exposes the provider beneath the view. This decorator narrows
// a model set; it is not a vendor, and it deliberately does NOT implement
// ProviderUnwrapper — see core.IdentityUnwrapper for why the two are separate.
//
// Without this, CanonicalName stops here and returns whatever Name() promotes
// to, which for a target registered through RegisterProviderAs is the routing
// alias. The cost-optimized strategy then prices against a key the catalog has
// never heard of and drops the target as unpriced.
func (p *indexedProvider) UnwrapIdentity() providers.Provider { return p.Provider }

// SupportsModel answers for routing, and is the same rule
// supportsModelForRoutingLocked applies: the index decides, and a declared
// AnyModelProvider is offered only the names the index gives no owner. The
// wrapped provider's own SupportsModel is deliberately not consulted.
func (p *indexedProvider) SupportsModel(model string) bool {
	owns, claimed := ownsModel(p.index, p.name, model)
	if claimed {
		return owns
	}
	return p.anyModel
}

// indexedStreamProvider is the view for a provider that streams. It exists so
// the widened view satisfies StreamProvider exactly when the provider beneath
// it does — the concurrency and circuit-breaker decorators compose around this
// one and assert that interface, and the strategies' streaming target ranking
// asserts it on the composed result.
type indexedStreamProvider struct {
	*indexedProvider
	stream providers.StreamProvider
}

func (p *indexedStreamProvider) CompleteStream(ctx context.Context, req providers.Request) (<-chan providers.StreamChunk, error) {
	return p.stream.CompleteStream(ctx, req)
}

// withIndexedModels wraps p in the routing-index view of its model set,
// preserving its streaming capability. index is a snapshot of
// modelIndex.exactProviders.
//
// p is wrapped even when the index is empty. Returning it bare — which is what
// this did, back when the view only ever WIDENED the provider's own answer —
// now hands the strategies a provider whose `return true` makes it a candidate
// for every model, which is the whole defect. An empty index means nothing is
// owned, so the view answers for the declared wildcards and nobody else.
func withIndexedModels(name string, p providers.Provider, index map[string][]string) providers.Provider {
	view := &indexedProvider{Provider: p, name: name, index: index, anyModel: servesAnyModel(p)}
	if sp, ok := providers.As[providers.StreamProvider](p); ok {
		return &indexedStreamProvider{indexedProvider: view, stream: sp}
	}
	return view
}

func (s *routingSnapshot) findProvider(model string) (providers.Provider, bool) {
	_, p, ok := findByModel[providers.Provider](s, s.index.exactProviders, model)
	return p, ok
}

func (s *routingSnapshot) findStreamingProviderMatch(model string) (string, providers.StreamProvider, bool) {
	return findByModel[providers.StreamProvider](s, s.index.exactStreamProviders, model)
}

func (s *routingSnapshot) findStreamingProvider(model string) (providers.StreamProvider, bool) {
	_, sp, ok := s.findStreamingProviderMatch(model)
	return sp, ok
}
