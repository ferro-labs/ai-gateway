package requestlog

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// listOrderWriters returns every dialect available here: SQLite always,
// Postgres when FERROGW_TEST_POSTGRES_DSN is set.
//
// Both are needed because the two planners hide the defect differently. SQLite
// reads the created_at index in reverse, whose keys carry the rowid, so it
// already answers in id order by accident; Postgres sorts, and its sort is not
// stable, so ties come back in whatever order the scan produced. The tiebreak is
// what makes the answer the same on both.
func listOrderWriters(t *testing.T) map[string]*SQLWriter {
	t.Helper()
	writers := make(map[string]*SQLWriter, 2)

	sqlite, err := NewSQLiteWriter(t.Context(), filepath.Join(t.TempDir(), "requests.db"))
	if err != nil {
		t.Fatalf("new sqlite writer: %v", err)
	}
	t.Cleanup(func() { _ = sqlite.Close() })
	writers["sqlite"] = sqlite

	dsn := os.Getenv("FERROGW_TEST_POSTGRES_DSN")
	if dsn == "" {
		return writers
	}
	pg, err := NewPostgresWriter(t.Context(), dsn)
	if err != nil {
		t.Fatalf("new postgres writer: %v", err)
	}
	if _, err := pg.db.ExecContext(t.Context(), "DELETE FROM request_logs"); err != nil {
		t.Fatalf("reset request_logs: %v", err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	writers["postgres"] = pg
	return writers
}

// GET /admin/logs pages through List, and one request writes several rows in the
// same clock tick — a before_request row and a terminal one, at minimum. Ordering
// by created_at alone leaves those rows' relative order to the planner, which is
// free to answer differently for the same query: a paging reader then sees one
// row twice and never sees another.
func TestSQLWriterList_StableOrderWithinOneTick(t *testing.T) {
	for name, writer := range listOrderWriters(t) {
		t.Run(name, func(t *testing.T) { assertStableListOrder(t, writer) })
	}
}

func assertStableListOrder(t *testing.T, w *SQLWriter) {
	t.Helper()

	const rows = 50
	tick := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	for i := range rows {
		if err := w.Write(t.Context(), Entry{
			TraceID:   fmt.Sprintf("trace-%03d", i),
			Stage:     "after_request",
			Model:     "gpt-4.1-mini",
			Provider:  "openai",
			CreatedAt: tick,
		}); err != nil {
			t.Fatalf("write row %d: %v", i, err)
		}
	}

	whole, err := w.List(t.Context(), Query{Limit: rows})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(whole.Data) != rows {
		t.Fatalf("got %d rows, want %d", len(whole.Data), rows)
	}

	// Newest first, and the rows share a timestamp, so the tiebreak must put
	// them in reverse insertion order: the last row written comes back first.
	for i, entry := range whole.Data {
		want := fmt.Sprintf("trace-%03d", rows-1-i)
		if entry.TraceID != want {
			t.Fatalf("row %d is %q, want %q — the listing is not newest-first", i, entry.TraceID, want)
		}
	}

	// Paging must reconstruct exactly that listing. Without the tiebreak a page
	// boundary can repeat a row and drop another, which no single-page read
	// would reveal.
	paged := make([]Entry, 0, rows)
	for offset := 0; offset < rows; offset += 10 {
		page, err := w.List(t.Context(), Query{Limit: 10, Offset: offset})
		if err != nil {
			t.Fatalf("list page at %d: %v", offset, err)
		}
		paged = append(paged, page.Data...)
	}
	if got, want := traceIDs(paged), traceIDs(whole.Data); !sameOrder(got, want) {
		t.Fatalf("paged listing %v does not reconstruct %v", got, want)
	}
}

func traceIDs(entries []Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.TraceID)
	}
	return out
}

func sameOrder(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
