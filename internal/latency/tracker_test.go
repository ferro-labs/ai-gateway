package latency

import (
	"sort"
	"testing"
	"time"
)

func TestTracker_Record_P50(t *testing.T) {
	tr := New(10)

	// Record 5 samples for "openai": 10, 20, 30, 40, 50 ms.
	for _, d := range []time.Duration{10, 20, 30, 40, 50} {
		tr.Record("openai", "m", d*time.Millisecond)
	}

	p50 := p50Of(tr, "openai")
	// Median of [10,20,30,40,50] is index 2 (0-based): 30 ms.
	if p50 != 30*time.Millisecond {
		t.Errorf("P50 = %v, want 30ms", p50)
	}
}

func TestTracker_P50_Empty(t *testing.T) {
	tr := New(10)
	if p50 := p50Of(tr, "unknown"); p50 != 0 {
		t.Errorf("P50 for unknown provider = %v, want 0", p50)
	}
}

func TestTracker_HasSamples(t *testing.T) {
	tr := New(10)
	if tr.HasSamples("openai") {
		t.Error("should have no samples before recording")
	}
	tr.Record("openai", "m", 5*time.Millisecond)
	if !tr.HasSamples("openai") {
		t.Error("should have samples after recording")
	}
}

func TestTracker_WindowEviction(t *testing.T) {
	window := 3
	tr := New(window)

	// Fill window with 1ms samples, then add two 100ms samples.
	for i := 0; i < window; i++ {
		tr.Record("p", "m", 1*time.Millisecond)
	}
	tr.Record("p", "m", 100*time.Millisecond)
	tr.Record("p", "m", 100*time.Millisecond)

	// After eviction the window contains [1ms, 100ms, 100ms].
	p50 := p50Of(tr, "p")
	// Median (index 1 of 3) should be 100ms.
	if p50 != 100*time.Millisecond {
		t.Errorf("P50 after eviction = %v, want 100ms", p50)
	}
}

func TestTracker_P50_Single(t *testing.T) {
	tr := New(10)
	tr.Record("p", "m", 42*time.Millisecond)
	if p50 := p50Of(tr, "p"); p50 != 42*time.Millisecond {
		t.Errorf("P50 single sample = %v, want 42ms", p50)
	}
}

func TestTracker_P50_Even(t *testing.T) {
	tr := New(10)
	// 4 samples: median at index len/2 = 2 → value 30ms when sorted [10,20,30,40].
	for _, d := range []time.Duration{40, 10, 30, 20} {
		tr.Record("p", "m", d*time.Millisecond)
	}
	sorted := []int{10, 20, 30, 40}
	sort.Ints(sorted)
	want := time.Duration(sorted[len(sorted)/2]) * time.Millisecond
	if p50 := p50Of(tr, "p"); p50 != want {
		t.Errorf("P50 even = %v, want %v", p50, want)
	}
}

func TestTracker_MultiProvider(t *testing.T) {
	tr := New(10)
	tr.Record("fast", "m", 10*time.Millisecond)
	tr.Record("slow", "m", 200*time.Millisecond)

	if p50Of(tr, "fast") >= p50Of(tr, "slow") {
		t.Errorf("expected fast (%v) < slow (%v)", p50Of(tr, "fast"), p50Of(tr, "slow"))
	}
}

func TestTracker_Retain(t *testing.T) {
	tr := New(10)
	tr.Record("keep", "m", 10*time.Millisecond)
	tr.Record("drop", "m", 20*time.Millisecond)

	tr.Retain([]string{"keep"})

	if tr.HasSamples("drop") {
		t.Error("dropped provider still has samples")
	}
	if p50 := p50Of(tr, "drop"); p50 != 0 {
		t.Errorf("dropped provider P50 = %v, want 0", p50)
	}
	if !tr.HasSamples("keep") {
		t.Error("retained provider lost its samples")
	}
	if p50 := p50Of(tr, "keep"); p50 != 10*time.Millisecond {
		t.Errorf("retained provider P50 = %v, want 10ms", p50)
	}

	// A config with no targets clears the tracker rather than retaining
	// everything, which is what an empty target list means.
	tr.Retain(nil)
	if tr.HasSamples("keep") {
		t.Error("Retain(nil) kept samples")
	}
}

// p50Of reads the memoized median for one target's only model.
func p50Of(tr *Tracker, target string) time.Duration {
	p50, _ := tr.Stats(target, "m")
	return p50
}

// fakeClock is a settable clock for the expiry tests.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func newClockedTracker(window int, ttl time.Duration) (*Tracker, *fakeClock) {
	clock := &fakeClock{now: time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)}
	return New(window, WithSampleTTL(ttl), WithClock(clock.Now)), clock
}

// A window nothing has refreshed for longer than the TTL is no history at all:
// under low traffic a p50 measured before an incident must not keep ranking the
// target as though nothing had changed.
func TestTracker_ExpiredHistoryIsNoHistory(t *testing.T) {
	tr, clock := newClockedTracker(10, time.Minute)
	tr.Record("p", "m", 10*time.Millisecond)

	clock.now = clock.now.Add(59 * time.Second)
	if _, ok := tr.Stats("p", "m"); !ok {
		t.Fatal("a sample inside the TTL must still count")
	}
	clock.now = clock.now.Add(2 * time.Second)
	if p50, ok := tr.Stats("p", "m"); ok {
		t.Fatalf("Stats = (%v, true) after the TTL, want no samples", p50)
	}
	if tr.HasSamples("p") {
		t.Fatal("HasSamples must not report an expired window")
	}
}

// Old samples leave the window as new ones arrive, so a target that slowed
// down is ranked on what it does now rather than on what it did an hour ago.
func TestTracker_RecordPrunesExpiredSamples(t *testing.T) {
	tr, clock := newClockedTracker(10, time.Minute)
	for range 5 {
		tr.Record("p", "m", 1*time.Millisecond)
	}
	clock.now = clock.now.Add(2 * time.Minute)
	tr.Record("p", "m", 100*time.Millisecond)

	if p50, _ := tr.Stats("p", "m"); p50 != 100*time.Millisecond {
		t.Fatalf("P50 = %v, want 100ms: the expired 1ms samples must not survive a Record", p50)
	}
}

// Samples are keyed by target AND upstream model, so a verbose or slow model
// on a target does not make a fast one on the same target look slow.
func TestTracker_SamplesAreKeyedByModel(t *testing.T) {
	tr := New(10)
	tr.Record("p", "slow-model", 500*time.Millisecond)
	tr.Record("p", "fast-model", 5*time.Millisecond)

	if p50, ok := tr.Stats("p", "fast-model"); !ok || p50 != 5*time.Millisecond {
		t.Fatalf("Stats(fast-model) = (%v, %v), want (5ms, true)", p50, ok)
	}
	if _, ok := tr.Stats("p", "never-seen"); ok {
		t.Fatal("a model with no samples on this target must read as unseen")
	}
	tr.Retain(nil)
	if tr.HasSamples("p") {
		t.Fatal("Retain(nil) must drop every model's window")
	}
}

// One expired sample beside one live sample: the expired one must not shape
// the median even though nothing has been recorded since it aged out.
func TestTracker_StatsIgnoresExpiredSamplesBesideLiveOnes(t *testing.T) {
	tr, clock := newClockedTracker(10, time.Minute)
	tr.Record("p", "m", 500*time.Millisecond)
	clock.now = clock.now.Add(45 * time.Second)
	tr.Record("p", "m", 5*time.Millisecond)
	clock.now = clock.now.Add(30 * time.Second) // the first sample is 75s old, the second 30s

	p50, ok := tr.Stats("p", "m")
	if !ok || p50 != 5*time.Millisecond {
		t.Fatalf("Stats = (%v, %v), want (5ms, true): the expired 500ms sample must not count", p50, ok)
	}
}
