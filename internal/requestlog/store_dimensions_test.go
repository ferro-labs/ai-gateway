package requestlog

import (
	"path/filepath"
	"testing"
	"time"
)

// writerWithResolvedModels seeds the rows a real request produces: a
// before_request row naming the model the CALLER asked for, then a terminal row
// naming the model and provider that answered.
func writerWithResolvedModels(t *testing.T, requests int) *SQLWriter {
	t.Helper()

	w, err := NewSQLiteWriter(t.Context(), filepath.Join(t.TempDir(), "dimensions.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	base := time.Now().UTC().Add(-time.Hour)
	for i := range requests {
		at := base.Add(time.Duration(i) * time.Minute)
		entries := []Entry{
			{Stage: "before_request", Model: "gpt-4o-mini", CreatedAt: at},
			{
				Stage:        "after_request",
				Model:        "gpt-4o-mini-2024-07-18",
				Provider:     "openai",
				TotalTokens:  10,
				PromptTokens: 10,
				CreatedAt:    at.Add(time.Second),
			},
		}
		for _, e := range entries {
			e.TraceID = "trace-" + string(rune('a'+i))
			if err := w.Write(t.Context(), e); err != nil {
				t.Fatalf("write %s: %v", e.Stage, err)
			}
		}
	}
	return w
}

// by_model and by_provider count requests, not logged rows.
//
// The logger writes one row per stage, so a dimension summing every row counted
// five requests as five under the model they were SENT as and five again under
// the model that ANSWERED — two buckets, each claiming the whole traffic, and a
// provider bucket named "unknown" holding one row per request because the
// before_request row has no provider yet.
func TestStatsDimensionsCountRequestsNotRows(t *testing.T) {
	const requests = 5
	w := writerWithResolvedModels(t, requests)

	stats, err := w.Stats(t.Context(), Query{})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}

	if got := stats.ByModel["gpt-4o-mini-2024-07-18"].Count; got != requests {
		t.Errorf("by_model[resolved].count = %d, want %d", got, requests)
	}
	if got, ok := stats.ByModel["gpt-4o-mini"]; ok {
		t.Errorf("by_model still holds a before_request-only bucket: %+v", got)
	}
	if got := stats.ByProvider["openai"].Count; got != requests {
		t.Errorf("by_provider[openai].count = %d, want %d", got, requests)
	}
	if got, ok := stats.ByProvider["unknown"]; ok {
		t.Errorf("by_provider still holds an unknown bucket from before_request rows: %+v", got)
	}

	// The dimension counts and the summary now answer the same question.
	if stats.TotalEntries != requests {
		t.Errorf("total_entries = %d, want %d", stats.TotalEntries, requests)
	}
	// ByStage is unchanged: it is the one breakdown that is about rows.
	if got := stats.ByStage["before_request"].Count; got != requests {
		t.Errorf("by_stage[before_request].count = %d, want %d", got, requests)
	}
}

// A caller who names a stage has asked about that stage, so nothing is
// narrowed — the same exemption the summary makes.
func TestStatsDimensionsHonourAnExplicitStage(t *testing.T) {
	const requests = 5
	w := writerWithResolvedModels(t, requests)

	stats, err := w.Stats(t.Context(), Query{Stage: "before_request"})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if got := stats.ByModel["gpt-4o-mini"].Count; got != requests {
		t.Errorf("by_model[requested].count = %d, want %d", got, requests)
	}
}

// The narrowing is a plain AND, so it must compose with the other filters
// without disturbing their bound arguments.
func TestStatsDimensionsCombineWithOtherFilters(t *testing.T) {
	const requests = 5
	w := writerWithResolvedModels(t, requests)

	stats, err := w.Stats(t.Context(), Query{Provider: "openai"})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if got := stats.ByModel["gpt-4o-mini-2024-07-18"].Count; got != requests {
		t.Errorf("by_model[resolved].count = %d, want %d", got, requests)
	}
	if got := stats.TotalEntries; got != requests {
		t.Errorf("total_entries = %d, want %d", got, requests)
	}
}
