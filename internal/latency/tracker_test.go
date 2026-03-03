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
		tr.Record("openai", d*time.Millisecond)
	}

	p50 := tr.P50("openai")
	// Median of [10,20,30,40,50] is index 2 (0-based): 30 ms.
	if p50 != 30*time.Millisecond {
		t.Errorf("P50 = %v, want 30ms", p50)
	}
}

func TestTracker_P50_Empty(t *testing.T) {
	tr := New(10)
	if p50 := tr.P50("unknown"); p50 != 0 {
		t.Errorf("P50 for unknown provider = %v, want 0", p50)
	}
}

func TestTracker_HasSamples(t *testing.T) {
	tr := New(10)
	if tr.HasSamples("openai") {
		t.Error("should have no samples before recording")
	}
	tr.Record("openai", 5*time.Millisecond)
	if !tr.HasSamples("openai") {
		t.Error("should have samples after recording")
	}
}

func TestTracker_WindowEviction(t *testing.T) {
	window := 3
	tr := New(window)

	// Fill window with 1ms samples, then add two 100ms samples.
	for i := 0; i < window; i++ {
		tr.Record("p", 1*time.Millisecond)
	}
	tr.Record("p", 100*time.Millisecond)
	tr.Record("p", 100*time.Millisecond)

	// After eviction the window contains [1ms, 100ms, 100ms].
	p50 := tr.P50("p")
	// Median (index 1 of 3) should be 100ms.
	if p50 != 100*time.Millisecond {
		t.Errorf("P50 after eviction = %v, want 100ms", p50)
	}
}

func TestTracker_P50_Single(t *testing.T) {
	tr := New(10)
	tr.Record("p", 42*time.Millisecond)
	if p50 := tr.P50("p"); p50 != 42*time.Millisecond {
		t.Errorf("P50 single sample = %v, want 42ms", p50)
	}
}

func TestTracker_P50_Even(t *testing.T) {
	tr := New(10)
	// 4 samples: median at index len/2 = 2 → value 30ms when sorted [10,20,30,40].
	for _, d := range []time.Duration{40, 10, 30, 20} {
		tr.Record("p", d*time.Millisecond)
	}
	sorted := []int{10, 20, 30, 40}
	sort.Ints(sorted)
	want := time.Duration(sorted[len(sorted)/2]) * time.Millisecond
	if p50 := tr.P50("p"); p50 != want {
		t.Errorf("P50 even = %v, want %v", p50, want)
	}
}

func TestTracker_MultiProvider(t *testing.T) {
	tr := New(10)
	tr.Record("fast", 10*time.Millisecond)
	tr.Record("slow", 200*time.Millisecond)

	if tr.P50("fast") >= tr.P50("slow") {
		t.Errorf("expected fast (%v) < slow (%v)", tr.P50("fast"), tr.P50("slow"))
	}
}
