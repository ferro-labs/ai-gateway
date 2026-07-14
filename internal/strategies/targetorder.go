package strategies

import (
	"math/rand"

	"github.com/ferro-labs/ai-gateway/providers"
)

// Shared helpers for the SelectTargets implementations. SelectTargets returns
// the ordered list of target virtual keys a strategy would try for a request,
// most-preferred first. It is the streaming counterpart to Execute's implicit
// provider selection: the gateway walks this order to resolve a
// streaming-capable provider, appending any registered fallback last.

// targetKeys returns the VirtualKey of every target, in declared order.
func targetKeys(targets []Target) []string {
	keys := make([]string, 0, len(targets))
	for _, t := range targets {
		keys = append(keys, t.VirtualKey)
	}
	return keys
}

// appendUniqueKey appends key to keys unless it is empty or already present.
func appendUniqueKey(keys []string, key string) []string {
	if key == "" {
		return keys
	}
	for _, existing := range keys {
		if existing == key {
			return keys
		}
	}
	return append(keys, key)
}

// appendRemainingTargetKeys appends every target key not already in keys,
// preserving declared order. Used to append fallback targets after a
// strategy-preferred prefix.
func appendRemainingTargetKeys(keys []string, targets []Target) []string {
	for _, t := range targets {
		keys = appendUniqueKey(keys, t.VirtualKey)
	}
	return keys
}

// weightedStartIndex picks a starting index into targets by weighted random
// selection (zero/negative weight counts as 1). The load-balance ordering
// rotates the target list from this index so the first attempted target is
// weight-biased while the rest remain available as fallbacks.
func weightedStartIndex(targets []Target) int {
	if len(targets) == 0 {
		return 0
	}
	totalWeight := 0.0
	for _, t := range targets {
		totalWeight += effectiveWeight(t.Weight)
	}
	if totalWeight <= 0 {
		return 0
	}
	r := rand.Float64() * totalWeight //nolint:gosec // G404: math/rand is fine for load-balancing weight selection, not security-sensitive
	cumulative := 0.0
	for i, t := range targets {
		cumulative += effectiveWeight(t.Weight)
		if r < cumulative {
			return i
		}
	}
	return len(targets) - 1
}

// streamCandidate reports whether the target key resolves to a registered,
// model-supporting, streaming-capable provider. Used by the latency- and
// cost-ordered strategies to decide which targets participate in ranking;
// non-candidates are still appended as trailing fallbacks.
//
// This resolves through the same lookup Execute uses. A provider
// wrapped in a circuit breaker always reports streaming-capable here even if
// its underlying provider is not; the gateway's resolution step re-checks the
// raw provider and skips it, so the finally resolved provider is unaffected.
func streamCandidate(lookup ProviderLookup, key, model string) bool {
	p, ok := lookup(key)
	if !ok || !p.SupportsModel(model) {
		return false
	}
	_, isStream := p.(providers.StreamProvider)
	return isStream
}
