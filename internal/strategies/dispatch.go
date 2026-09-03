package strategies

import (
	"math/rand"
)

// weightedPick selects an element by weighted random sampling using weight for
// each element's share. Returns false when the total weight is zero.
//
// A zero-weight element is skipped outright rather than being allowed to win a
// boundary comparison: with weight 0 its cumulative contribution is nothing, so
// an element that follows a drained one must never be attributed to it.
func weightedPick[T any](items []T, weight func(T) float64) (T, bool) {
	return weightedPickAt(items, weight, rand.Float64()) //nolint:gosec // G404: math/rand is fine for load-balancing weight selection, not security-sensitive
}

// weightedPickAt is weightedPick with the draw supplied as unit in [0, 1).
func weightedPickAt[T any](items []T, weight func(T) float64, unit float64) (T, bool) {
	total := 0.0
	for _, it := range items {
		total += weight(it)
	}
	if total <= 0 {
		var zero T
		return zero, false
	}

	r := unit * total
	cumulative := 0.0
	var last T
	for _, it := range items {
		w := weight(it)
		if w <= 0 {
			continue
		}
		last = it
		cumulative += w
		if r < cumulative {
			return it, true
		}
	}
	// Only reachable if floating-point summation left r at or past the total.
	// Return the last POSITIVELY weighted element, never merely the last one.
	return last, true
}
