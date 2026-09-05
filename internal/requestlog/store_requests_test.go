package requestlog

import (
	"path/filepath"
	"testing"
	"time"
)

// writerWithTraffic seeds a store with the rows a request logger really writes:
// one before_request row per request, then one terminal row. Three requests
// succeed, one fails.
func writerWithTraffic(t *testing.T) *SQLWriter {
	t.Helper()

	w, err := NewSQLiteWriter(t.Context(), filepath.Join(t.TempDir(), "requests.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Errorf("close request log writer: %v", err)
		}
	})

	base := time.Now().UTC().Add(-time.Hour)
	write := func(trace, stage string, tokens int, errMessage string, at time.Time) {
		t.Helper()
		if err := w.Write(t.Context(), Entry{
			TraceID:      trace,
			Stage:        stage,
			Model:        "stub-model",
			Provider:     "ollama",
			TotalTokens:  tokens,
			PromptTokens: tokens,
			ErrorMessage: errMessage,
			CreatedAt:    at,
		}); err != nil {
			t.Fatalf("write %s/%s: %v", trace, stage, err)
		}
	}

	for i, trace := range []string{"a", "b", "c", "d"} {
		at := base.Add(time.Duration(i) * time.Minute)
		// A before_request row carries no provider result yet: no tokens, no
		// outcome. Written for every request, including the one that fails.
		write(trace, "before_request", 0, "", at)
		if trace == "d" {
			write(trace, "on_error", 0, "boom", at.Add(time.Second))
			continue
		}
		write(trace, "after_request", 10, "", at.Add(time.Second))
	}
	return w
}

// TestListTerminalStagesReturnsOneRowPerRequest is the store half of the
// phantom half-requests the dashboard listed: eight rows for four requests.
func TestListTerminalStagesReturnsOneRowPerRequest(t *testing.T) {
	w := writerWithTraffic(t)

	all, err := w.List(t.Context(), Query{})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if all.Total != 8 {
		t.Fatalf("expected 8 logged rows for 4 requests, got %d", all.Total)
	}

	requests, err := w.List(t.Context(), Query{Stages: TerminalStages()})
	if err != nil {
		t.Fatalf("list terminal: %v", err)
	}
	if requests.Total != 4 || len(requests.Data) != 4 {
		t.Fatalf("expected 4 rows for 4 requests, got total=%d returned=%d", requests.Total, len(requests.Data))
	}

	seen := map[string]bool{}
	for _, entry := range requests.Data {
		if seen[entry.TraceID] {
			t.Errorf("trace %s listed more than once", entry.TraceID)
		}
		seen[entry.TraceID] = true
		if entry.Stage == "before_request" {
			t.Errorf("trace %s: a before_request row survived the terminal filter", entry.TraceID)
		}
	}
}

// TestListStagesCombinesWithOtherFilters guards the generated IN clause against
// binding its placeholders out of order once another predicate is present.
func TestListStagesCombinesWithOtherFilters(t *testing.T) {
	w := writerWithTraffic(t)

	result, err := w.List(t.Context(), Query{Stages: TerminalStages(), Provider: "ollama", Model: "stub-model"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if result.Total != 4 {
		t.Fatalf("expected 4 requests, got %d", result.Total)
	}

	none, err := w.List(t.Context(), Query{Stages: TerminalStages(), Provider: "openai"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if none.Total != 0 {
		t.Fatalf("expected no rows for an unused provider, got %d", none.Total)
	}
}

// TestStatsSummaryCountsRequestsNotRows covers the consumer half: an API caller
// reading summary.total_entries as the request count was wrong by ~2x, and
// dividing error_entries by it halved every failure rate.
func TestStatsSummaryCountsRequestsNotRows(t *testing.T) {
	w := writerWithTraffic(t)

	stats, err := w.Stats(t.Context(), Query{})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}

	if stats.TotalEntries != 4 {
		t.Errorf("total_entries: want 4 requests, got %d", stats.TotalEntries)
	}
	if stats.ErrorEntries != 1 {
		t.Errorf("error_entries: want 1, got %d", stats.ErrorEntries)
	}
	if rate := float64(stats.ErrorEntries) / float64(stats.TotalEntries) * 100; rate != 25 {
		t.Errorf("error rate: want 25%%, got %.1f%%", rate)
	}
	// The breakdown still describes every row — it is the point of ByStage, and
	// an operator asking "how many are in flight" reads it there.
	if got := stats.ByStage["before_request"].Count; got != 4 {
		t.Errorf("by_stage keeps every row: want 4 before_request, got %d", got)
	}
}

// TestStatsSummaryHonoursAnExplicitStage keeps "count each request once" from
// collapsing into "terminal stages only", which would answer zero for a caller
// who asked about the stage that has no outcome yet.
func TestStatsSummaryHonoursAnExplicitStage(t *testing.T) {
	w := writerWithTraffic(t)

	stats, err := w.Stats(t.Context(), Query{Stage: "before_request"})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.TotalEntries != 4 {
		t.Errorf("a stage the caller named counts its own rows: want 4, got %d", stats.TotalEntries)
	}
}
