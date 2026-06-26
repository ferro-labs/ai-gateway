package latency

import (
	"testing"
	"time"
)

// BenchmarkP50 measures the read path of the median lookup. With the median
// memoized on Record, P50 is an O(1), allocation-free read rather than a
// per-call copy + sort over the full window.
func BenchmarkP50(b *testing.B) {
	tr := New(100)
	// Fill the window so a naive implementation would sort 100 samples per call.
	for i := 0; i < 100; i++ {
		tr.Record("openai", time.Duration(100-i)*time.Millisecond)
	}

	b.ReportAllocs()
	b.ResetTimer()
	var sink time.Duration
	for i := 0; i < b.N; i++ {
		sink = tr.P50("openai")
	}
	_ = sink
}

// BenchmarkStats measures the combined single-RLock read path used to avoid a
// double RLock when a caller needs both the median and sample presence.
func BenchmarkStats(b *testing.B) {
	tr := New(100)
	for i := 0; i < 100; i++ {
		tr.Record("openai", time.Duration(100-i)*time.Millisecond)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tr.Stats("openai")
	}
}
