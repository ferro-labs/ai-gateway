// Package latency provides a thread-safe rolling-window latency tracker used
// by the least-latency routing strategy to pick the fastest provider.
package latency

import (
	"sort"
	"sync"
	"time"
)

const defaultWindowSize = 100

// Tracker records per-provider latency samples in a fixed-size rolling window
// and exposes percentile statistics for routing decisions.
//
// The P50 median is memoized on Record so reads (P50/Stats) are O(1) and
// allocation-free on the hot routing path; the sort cost is paid once per
// observation on the lower-frequency write path instead of once per candidate
// target per request.
type Tracker struct {
	mu         sync.RWMutex
	samples    map[string][]time.Duration
	medians    map[string]time.Duration
	windowSize int
}

// New creates a Tracker with the given window size.
// If windowSize is zero or negative, defaultWindowSize (100) is used.
func New(windowSize int) *Tracker {
	if windowSize <= 0 {
		windowSize = defaultWindowSize
	}
	return &Tracker{
		samples:    make(map[string][]time.Duration),
		medians:    make(map[string]time.Duration),
		windowSize: windowSize,
	}
}

// Record adds a latency observation for the named provider.
// The oldest sample is dropped when the window is full, keeping only the
// most recent windowSize observations. The memoized median is recomputed so
// subsequent P50/Stats reads need no sorting.
func (t *Tracker) Record(provider string, d time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.samples[provider] = append(t.samples[provider], d)
	window := t.samples[provider]
	if len(window) > t.windowSize {
		window = window[len(window)-t.windowSize:]
		t.samples[provider] = window
	}
	t.medians[provider] = computeMedian(window)
}

// computeMedian returns the median of src without mutating it. The result
// matches the upper-middle element of the ascending order (index len/2),
// preserving the original P50 semantics.
func computeMedian(src []time.Duration) time.Duration {
	if len(src) == 0 {
		return 0
	}
	sorted := make([]time.Duration, len(src))
	copy(sorted, src)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2]
}

// P50 returns the median (50th-percentile) latency for the given provider.
// Returns 0 if no samples have been recorded yet. This is an O(1) read of the
// median computed at Record time.
func (t *Tracker) P50(provider string) time.Duration {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.medians[provider]
}

// HasSamples reports whether at least one sample has been recorded for the
// given provider.
func (t *Tracker) HasSamples(provider string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.samples[provider]) > 0
}

// Stats returns the memoized P50 and whether any samples exist for the
// provider in a single read-lock acquisition. Prefer this over separate
// P50 + HasSamples calls on the routing hot path to avoid a double RLock.
func (t *Tracker) Stats(provider string) (p50 time.Duration, hasSamples bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.medians[provider], len(t.samples[provider]) > 0
}
