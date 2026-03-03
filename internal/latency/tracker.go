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
type Tracker struct {
	mu         sync.RWMutex
	samples    map[string][]time.Duration
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
		windowSize: windowSize,
	}
}

// Record adds a latency observation for the named provider.
// The oldest sample is dropped when the window is full, keeping only the
// most recent windowSize observations.
func (t *Tracker) Record(provider string, d time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.samples[provider] = append(t.samples[provider], d)
	if len(t.samples[provider]) > t.windowSize {
		t.samples[provider] = t.samples[provider][len(t.samples[provider])-t.windowSize:]
	}
}

// P50 returns the median (50th-percentile) latency for the given provider.
// Returns 0 if no samples have been recorded yet.
func (t *Tracker) P50(provider string) time.Duration {
	t.mu.RLock()
	src := t.samples[provider]
	if len(src) == 0 {
		t.mu.RUnlock()
		return 0
	}
	// Copy before releasing the lock to avoid sorting the live slice.
	sorted := make([]time.Duration, len(src))
	copy(sorted, src)
	t.mu.RUnlock()

	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2]
}

// HasSamples reports whether at least one sample has been recorded for the
// given provider.
func (t *Tracker) HasSamples(provider string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.samples[provider]) > 0
}
