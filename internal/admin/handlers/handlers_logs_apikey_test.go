package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/requestlog"
)

// attributedTraffic is one gateway's log as two credentials and one
// unauthenticated caller left it.
func attributedTraffic() *fakeLogReader {
	now := time.Now().UTC()
	return &fakeLogReader{entries: []requestlog.Entry{
		{TraceID: "a-1", Stage: "after_request", Model: "gpt-4o", Provider: "openai", APIKeyID: "key-a", TotalTokens: 10, CreatedAt: now.Add(-4 * time.Minute)},
		{TraceID: "a-2", Stage: "on_error", Model: "gpt-4o", Provider: "openai", APIKeyID: "key-a", ErrorMessage: "boom", CreatedAt: now.Add(-3 * time.Minute)},
		{TraceID: "b-1", Stage: "after_request", Model: "gpt-4o", Provider: "groq", APIKeyID: "key-b", TotalTokens: 20, CreatedAt: now.Add(-2 * time.Minute)},
		{TraceID: "anon", Stage: "after_request", Model: "gpt-4o", Provider: "openai", TotalTokens: 5, CreatedAt: now.Add(-time.Minute)},
	}}
}

type logsPayload struct {
	Data    []requestlog.Entry `json:"data"`
	Summary struct {
		TotalEntries    int `json:"total_entries"`
		ReturnedEntries int `json:"returned_entries"`
	} `json:"summary"`
	Filters struct {
		APIKeyID string `json:"api_key_id"`
	} `json:"filters"`
}

func listAttributedLogs(t *testing.T, target string) logsPayload {
	t.Helper()

	h, r := setupTestRouterWithLogs(attributedTraffic())
	adminKey := createAdminKey(t, h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodGet, target, "", adminKey))
	if w.Code != http.StatusOK {
		t.Fatalf("%s: expected 200, got %d: %s", target, w.Code, w.Body.String())
	}

	var payload logsPayload
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("%s: decode: %v", target, err)
	}
	return payload
}

// TestLogsFilterByAPIKey covers the question the column exists to answer.
// Recording which credential served a request is worth nothing until an
// operator can select on it.
func TestLogsFilterByAPIKey(t *testing.T) {
	cases := []struct {
		name   string
		target string
		want   []string
	}{
		{
			name:   "absent selects every row, whoever served it",
			target: "/admin/logs",
			want:   []string{"anon", "b-1", "a-2", "a-1"},
		},
		{
			name:   "one credential selects its own rows across every stage",
			target: "/admin/logs?api_key_id=key-a",
			want:   []string{"a-2", "a-1"},
		},
		{
			name:   "another credential selects its row and none of the first's",
			target: "/admin/logs?api_key_id=key-b",
			want:   []string{"b-1"},
		},
		{
			// A gateway running without key auth records every row this way.
			// Selectable by a reserved value because the parameter's empty
			// value already means "no filter" on this endpoint.
			name:   "the reserved value selects the rows naming no credential",
			target: "/admin/logs?api_key_id=none",
			want:   []string{"anon"},
		},
		{
			// Deliberate, not incidental: an empty value is absent here as it
			// is for model, provider and stage, so a client that always sets
			// the parameter does not silently narrow the listing to the
			// unattributed rows.
			name:   "an empty value is no filter rather than a second spelling of none",
			target: "/admin/logs?api_key_id=",
			want:   []string{"anon", "b-1", "a-2", "a-1"},
		},
		{
			// A revoked or deleted credential keeps its rows, so an id the key
			// store no longer knows is a legitimate filter rather than a 400 —
			// and one that served nothing must return nothing, never the lot.
			name:   "an id that served nothing returns nothing rather than everything",
			target: "/admin/logs?api_key_id=key-gone",
			want:   nil,
		},
		{
			name:   "it narrows alongside the other filters rather than replacing them",
			target: "/admin/logs?api_key_id=key-a&stage=on_error",
			want:   []string{"a-2"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := listAttributedLogs(t, tc.target)

			got := make([]string, 0, len(payload.Data))
			for _, entry := range payload.Data {
				got = append(got, entry.TraceID)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("trace ids = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("trace ids = %v, want %v", got, tc.want)
				}
			}
			if payload.Summary.TotalEntries != len(tc.want) {
				t.Errorf("total_entries = %d, want %d", payload.Summary.TotalEntries, len(tc.want))
			}
		})
	}
}

// TestLogsNeverDiscloseTheCredential holds the one line this feature must not
// cross. The row carries an opaque id so usage is attributable; the secret it
// identifies has no business leaving the key store, and a filtered listing is
// the natural place for one to be echoed back by accident.
func TestLogsNeverDiscloseTheCredential(t *testing.T) {
	h, r := setupTestRouterWithLogs(attributedTraffic())
	adminKey := createAdminKey(t, h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodGet, "/admin/logs?api_key_id=key-a", "", adminKey))

	if body := w.Body.String(); strings.Contains(body, adminKey.Key) {
		t.Fatal("the request log response quoted the credential it was fetched with")
	}
}

// TestLogsEchoesTheAPIKeyFilterAsAsked keeps `none` distinguishable from "no
// filter" on the way out: both resolve to the same empty identity internally,
// and echoing the resolved form would make the two look identical to a client.
func TestLogsEchoesTheAPIKeyFilterAsAsked(t *testing.T) {
	for _, tc := range []struct {
		target string
		want   string
	}{
		{target: "/admin/logs", want: ""},
		{target: "/admin/logs?api_key_id=none", want: "none"},
		{target: "/admin/logs?api_key_id=key-a", want: "key-a"},
	} {
		payload := listAttributedLogs(t, tc.target)
		if payload.Filters.APIKeyID != tc.want {
			t.Errorf("%s: api_key_id echo = %q, want %q", tc.target, payload.Filters.APIKeyID, tc.want)
		}
	}
}
