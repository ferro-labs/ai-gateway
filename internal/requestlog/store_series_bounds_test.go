package requestlog

import (
	"path/filepath"
	"testing"
	"time"
)

// appendSeries owns its bucket bound rather than trusting the caller to have
// checked. Stats gates the call on SeriesBuckets > 0, so today no caller can
// reach it with a hostile count — but the guard living one level up is the
// kind that a second caller silently loses. A zero divides by zero deriving
// the bucket width; a negative panics in make.
func TestAppendSeries_ClampsHostileBucketCounts(t *testing.T) {
	w, err := NewSQLiteWriter(t.Context(), filepath.Join(t.TempDir(), "requests.db"))
	if err != nil {
		t.Fatalf("new sqlite writer: %v", err)
	}
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Errorf("close request log writer: %v", err)
		}
	})

	since := time.Now().Add(-time.Hour)
	for _, tc := range []struct {
		name    string
		buckets int
		want    int
	}{
		{name: "zero", buckets: 0, want: 1},
		{name: "negative", buckets: -5, want: 1},
		{name: "far past the cap", buckets: maxSeriesBuckets * 100, want: maxSeriesBuckets},
		{name: "inside the cap", buckets: 12, want: 12},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var result StatsResult
			query := Query{SeriesBuckets: tc.buckets, Since: &since}
			if err := w.appendSeries(t.Context(), &result, "", nil, query); err != nil {
				t.Fatalf("appendSeries: %v", err)
			}
			if len(result.Series) != tc.want {
				t.Errorf("series points = %d, want %d", len(result.Series), tc.want)
			}
		})
	}
}
