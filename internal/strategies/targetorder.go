package strategies

import (
	"math/rand"
)

// Shared helpers for the SelectTargets implementations. SelectTargets returns
// the ordered list of target virtual keys a strategy would try for a request,
// most-preferred first. The gateway's request pipeline walks that one order on
// every surface, so a config orders its targets identically whether the request
// streams or not.

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
// selection. The load-balance ordering rotates the target list from this index
// so the first attempted target is weight-biased while the rest remain
// available as fallbacks.
//
// A zero weight means zero: that target is never the start index, so under
// loadbalance — where the pipeline commits to the first key — it receives no
// traffic at all. That is the whole point of the value; draining a target ahead
// of revoking its credential is what it is for.
//
// The second return is false when no target carries a positive weight. Callers
// report that as "nothing here can serve this" rather than falling back to
// index 0, which would hand traffic to a target the operator drained.
// ValidateConfig already rejects an all-zero config, so this is reachable only
// when the positively-weighted targets were filtered out for some other reason —
// most often because none of them serves the requested model.
func weightedStartIndex(targets []Target) (int, bool) {
	return weightedStartIndexAt(targets, rand.Float64()) //nolint:gosec // G404: math/rand is fine for load-balancing weight selection, not security-sensitive
}

// weightedStartIndexAt is weightedStartIndex with the draw supplied: unit in
// [0, 1) selects the target whose cumulative weight share contains it, so a
// sticky hash lands the same key on the same target every time.
func weightedStartIndexAt(targets []Target, unit float64) (int, bool) {
	totalWeight := 0.0
	for _, t := range targets {
		totalWeight += positiveWeight(t.Weight)
	}
	if totalWeight <= 0 {
		return 0, false
	}
	r := unit * totalWeight
	cumulative := 0.0
	last := 0
	for i, t := range targets {
		w := positiveWeight(t.Weight)
		if w == 0 {
			continue
		}
		last = i
		cumulative += w
		if r < cumulative {
			return i, true
		}
	}
	// Only reachable if floating-point summation left r at or past the total.
	// Return the last POSITIVELY weighted index, never merely the last one — the
	// last target may be the drained one.
	return last, true
}

// positiveWeight clamps a weight to the non-negative range. ValidateConfig
// rejects a negative weight outright, so this only guards a Gateway built from a
// programmatic Config that skipped validation; it must not resurrect a zero.
func positiveWeight(w float64) float64 {
	if w < 0 {
		return 0
	}
	return w
}

// routableCandidate reports whether the target key resolves to a registered
// provider that serves model. Used by the latency- and cost-ordered strategies
// to decide which targets participate in ranking.
//
// It deliberately asks nothing about the SURFACE. This used to also require
// providers.StreamProvider, because streaming was the only caller — which made
// the ordering a streaming answer that the non-streaming paths could not reuse
// without silently dropping every target that does not stream. Which capability
// a target needs is the executing surface's question, and the gateway's own
// resolution step is where it is asked; a strategy answers only "in what order
// would you try these".
func routableCandidate(lookup ProviderLookup, key, model string) bool {
	p, ok := lookup(key)
	return ok && p.SupportsModel(model)
}
