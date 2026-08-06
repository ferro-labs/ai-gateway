package requestlog

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/sqldb"
)

// Query.APIKeyID is what turns a recorded credential id into an answerable
// question: an operator asking "which key spent this" filters on it rather than
// paging through every row looking for one id.
//
// The two rows that name no credential are the interesting half. An
// unauthenticated request records an empty value; a row written before the
// column existed holds NULL. Storage keeps them distinct and the filter folds
// them, so every row an operator can see is reachable by some filter value.
func TestSQLiteWriter_ListFiltersByAPIKey(t *testing.T) {
	w, err := NewSQLiteWriter(t.Context(), filepath.Join(t.TempDir(), "requests.db"))
	if err != nil {
		t.Fatalf("new sqlite writer: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	base := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	written := []Entry{
		{TraceID: "a-1", Stage: stageAfterRequest, Model: "gpt-4o", Provider: "openai", APIKeyID: "key-a", CreatedAt: base},
		{TraceID: "a-2", Stage: stageOnError, Model: "gpt-4o", Provider: "openai", APIKeyID: "key-a", CreatedAt: base.Add(time.Minute)},
		{TraceID: "b-1", Stage: stageAfterRequest, Model: "gpt-4o", Provider: "openai", APIKeyID: "key-b", CreatedAt: base.Add(2 * time.Minute)},
		{TraceID: "anon", Stage: stageAfterRequest, Model: "gpt-4o", Provider: "openai", CreatedAt: base.Add(3 * time.Minute)},
	}
	for _, entry := range written {
		if err := w.Write(t.Context(), entry); err != nil {
			t.Fatalf("write %s: %v", entry.TraceID, err)
		}
	}

	// Written past the Entry struct on purpose: Write always binds a string, so
	// a NULL row can only be produced the way one really arose — by a gateway
	// that recorded the row before migration 5 added the column.
	insert := sqldb.Bind(w.dialect, `INSERT INTO request_logs(trace_id, stage, model, provider, prompt_tokens, completion_tokens, total_tokens, error_message, created_at)
	VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if _, err := w.db.ExecContext(t.Context(), insert,
		"legacy", stageAfterRequest, "gpt-4o", "openai", 0, 0, 0, "", base.Add(4*time.Minute)); err != nil {
		t.Fatalf("insert pre-migration row: %v", err)
	}

	id := func(value string) *string { return &value }

	cases := []struct {
		name   string
		filter *string
		want   []string
	}{
		{
			name:   "no filter returns every row, attributed or not",
			filter: nil,
			want:   []string{"a-1", "a-2", "b-1", "anon", "legacy"},
		},
		{
			name:   "one key returns that key's rows across every stage",
			filter: id("key-a"),
			want:   []string{"a-1", "a-2"},
		},
		{
			name:   "another key returns its own row and none of the first key's",
			filter: id("key-b"),
			want:   []string{"b-1"},
		},
		{
			// Both halves of "names no credential": the empty value an
			// unauthenticated request records, and the NULL of a row written
			// before the column existed.
			name:   "the empty identity returns the unauthenticated and pre-migration rows",
			filter: id(""),
			want:   []string{"anon", "legacy"},
		},
		{
			// A deleted key still has rows. Answering nothing is the correct
			// answer for an id that never served a request, and the filter must
			// not fall back to returning everything.
			name:   "an id that served nothing returns nothing rather than everything",
			filter: id("key-gone"),
			want:   nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := w.List(t.Context(), Query{Limit: 100, APIKeyID: tc.filter})
			if err != nil {
				t.Fatalf("list: %v", err)
			}

			got := make([]string, 0, len(result.Data))
			for _, entry := range result.Data {
				got = append(got, entry.TraceID)
			}
			if !sameSet(got, tc.want) {
				t.Errorf("trace ids = %v, want %v", got, tc.want)
			}
			// Total drives the pagination caption, and it is computed by a
			// second statement sharing the same WHERE clause. A count that
			// ignored the filter would report a page of two rows out of five.
			if result.Total != len(tc.want) {
				t.Errorf("total = %d, want %d", result.Total, len(tc.want))
			}
		})
	}
}

func sameSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	remaining := make(map[string]int, len(want))
	for _, value := range want {
		remaining[value]++
	}
	for _, value := range got {
		remaining[value]--
		if remaining[value] < 0 {
			return false
		}
	}
	return true
}
