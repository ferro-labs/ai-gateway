// Package latency provides a thread-safe rolling-window latency tracker used
// by the least-latency routing strategy to pick the fastest provider.
package latency

import (
	"sort"
	"sync"
	"time"
)

const (
	defaultWindowSize = 100
	// DefaultSampleTTL bounds how long a sample counts. A window is
	// request-count decay: under low traffic its samples could otherwise be
	// hours old and still rank a target on a number from before an incident.
	DefaultSampleTTL = 5 * time.Minute
)

// Option configures a Tracker.
type Option func(*Tracker)

// WithSampleTTL sets how long a sample counts; zero or negative keeps the
// default.
func WithSampleTTL(ttl time.Duration) Option {
	return func(t *Tracker) {
		if ttl > 0 {
			t.ttl = ttl
		}
	}
}

// WithClock replaces the wall clock, so expiry can be tested deterministically.
func WithClock(now func() time.Time) Option {
	return func(t *Tracker) {
		if now != nil {
			t.now = now
		}
	}
}

// sampleKey is what a window describes: one upstream model on one routing
// target. Keying on the target alone mixed a verbose or slow model's numbers
// into a fast one's on the same target.
type sampleKey struct{ target, model string }

type sample struct {
	at time.Time
	d  time.Duration
}

type window struct {
	samples []sample
	median  time.Duration
}

// Tracker records per-target, per-model latency samples in a fixed-size,
// time-bounded rolling window and exposes the median for routing decisions.
//
// The median is memoized on Record so Stats is O(1) and allocation-free on the
// hot routing path; the sort cost is paid once per observation on the
// lower-frequency write path instead of once per candidate target per request.
type Tracker struct {
	mu         sync.RWMutex
	windows    map[sampleKey]*window
	windowSize int
	ttl        time.Duration
	now        func() time.Time
}

// New creates a Tracker with the given window size.
// If windowSize is zero or negative, defaultWindowSize (100) is used.
func New(windowSize int, opts ...Option) *Tracker {
	if windowSize <= 0 {
		windowSize = defaultWindowSize
	}
	t := &Tracker{
		windows:    make(map[sampleKey]*window),
		windowSize: windowSize,
		ttl:        DefaultSampleTTL,
		now:        time.Now,
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// Record adds a latency observation for model on target. Samples older than
// the TTL and, beyond that, the oldest past the window size are dropped, and
// the memoized median is recomputed over what remains.
func (t *Tracker) Record(target, model string, d time.Duration) {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	key := sampleKey{target, model}
	w := t.windows[key]
	if w == nil {
		w = &window{}
		t.windows[key] = w
	}
	live := w.samples[t.liveFrom(w.samples, now):]
	live = append(live, sample{at: now, d: d})
	if len(live) > t.windowSize {
		live = live[len(live)-t.windowSize:]
	}
	w.samples = live
	w.median = computeMedian(live)
}

// liveFrom returns the index of the first sample recorded within the TTL.
// Samples are appended in time order, so the live set is always a suffix.
func (t *Tracker) liveFrom(samples []sample, now time.Time) int {
	cutoff := now.Add(-t.ttl)
	return sort.Search(len(samples), func(i int) bool { return !samples[i].at.Before(cutoff) })
}

// Retain drops every target not named in keep.
//
// Samples are keyed by routing target, and a config reload can remove a target.
// Without this its window outlived it: the tracker grew for the life of the
// process, and — worse — a target removed and later re-added resumed against a
// stale median measured before the change, which is precisely the ranking the
// least-latency strategy reads.
//
// Passing an empty slice clears the tracker, which is what a config with no
// targets means.
func (t *Tracker) Retain(keep []string) {
	kept := make(map[string]struct{}, len(keep))
	for _, target := range keep {
		kept[target] = struct{}{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for key := range t.windows {
		if _, ok := kept[key.target]; !ok {
			delete(t.windows, key)
		}
	}
}

// computeMedian returns the median of src without mutating it. The result
// matches the upper-middle element of the ascending order (index len/2),
// preserving the original P50 semantics.
func computeMedian(src []sample) time.Duration {
	if len(src) == 0 {
		return 0
	}
	sorted := make([]time.Duration, len(src))
	for i, s := range src {
		sorted[i] = s.d
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2]
}

// HasSamples reports whether any model on target has a live sample.
func (t *Tracker) HasSamples(target string) bool {
	now := t.now()
	t.mu.RLock()
	defer t.mu.RUnlock()
	for key, w := range t.windows {
		if key.target == target && t.isLive(w, now) {
			return true
		}
	}
	return false
}

// Stats returns the memoized median for model on target and whether the
// window is live. A window whose newest sample is older than the TTL reads as
// no samples at all: it is a number from before whatever changed, and the
// strategy treats the target as unseen and measures it again. Samples that
// aged out ahead of a newer one are pruned at the next Record.
func (t *Tracker) Stats(target, model string) (p50 time.Duration, hasSamples bool) {
	now := t.now()
	t.mu.RLock()
	defer t.mu.RUnlock()
	w := t.windows[sampleKey{target, model}]
	if w == nil || !t.isLive(w, now) {
		return 0, false
	}
	return w.median, true
}

func (t *Tracker) isLive(w *window, now time.Time) bool {
	n := len(w.samples)
	return n > 0 && !w.samples[n-1].at.Before(now.Add(-t.ttl))
}
