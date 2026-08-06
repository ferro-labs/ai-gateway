package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/requestlog"
)

// stagedTraffic is what a request logger really writes: a before_request row per
// request with no provider and no tokens, then one terminal row.
func stagedTraffic() *fakeLogReader {
	now := time.Now().UTC()
	entries := make([]requestlog.Entry, 0, 6)
	for i, trace := range []string{"a", "b", "c"} {
		at := now.Add(-time.Duration(i+1) * time.Minute)
		entries = append(entries,
			requestlog.Entry{TraceID: trace, Stage: "before_request", Model: "gpt-4", CreatedAt: at},
			requestlog.Entry{TraceID: trace, Stage: "after_request", Model: "gpt-4", Provider: "openai", TotalTokens: 10, CreatedAt: at.Add(time.Second)},
		)
	}
	return &fakeLogReader{entries: entries}
}

func listLogsAt(t *testing.T, target string) (int, []requestlog.Entry) {
	t.Helper()

	h, r := setupTestRouterWithLogs(stagedTraffic())
	adminKey := createAdminKey(t, h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodGet, target, "", adminKey))
	if w.Code != http.StatusOK {
		t.Fatalf("%s: expected 200, got %d: %s", target, w.Code, w.Body.String())
	}

	var payload struct {
		Data    []requestlog.Entry `json:"data"`
		Summary struct {
			TotalEntries int `json:"total_entries"`
		} `json:"summary"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("%s: decode: %v", target, err)
	}
	return payload.Summary.TotalEntries, payload.Data
}

// TestLogsDefaultListsOneRowPerRequest is the reproduction: three requests were
// listed as six, each real row shadowed by a provider-less, zero-token copy.
func TestLogsDefaultListsOneRowPerRequest(t *testing.T) {
	total, data := listLogsAt(t, "/admin/logs")

	if total != 3 || len(data) != 3 {
		t.Fatalf("3 requests must list as 3 rows, got total=%d returned=%d", total, len(data))
	}
	for _, entry := range data {
		if entry.Stage == "before_request" {
			t.Errorf("trace %s: a half-request reached the default listing", entry.TraceID)
		}
		if entry.Provider == "" {
			t.Errorf("trace %s: listed with no provider, which is the phantom row's signature", entry.TraceID)
		}
	}
}

// TestLogsStageAllRestoresEveryLoggedRow keeps the escape hatch honest: the
// operator debugging one request's path through the stages can still see it.
func TestLogsStageAllRestoresEveryLoggedRow(t *testing.T) {
	total, data := listLogsAt(t, "/admin/logs?stage=all")

	if total != 6 || len(data) != 6 {
		t.Fatalf("stage=all must return every logged row, got total=%d returned=%d", total, len(data))
	}
}

// TestLogsNamedStageStillFilters keeps the default from swallowing the existing
// per-stage filter, including the stage the default excludes.
func TestLogsNamedStageStillFilters(t *testing.T) {
	total, data := listLogsAt(t, "/admin/logs?stage=before_request")

	if total != 3 || len(data) != 3 {
		t.Fatalf("expected the 3 before_request rows, got total=%d returned=%d", total, len(data))
	}
	for _, entry := range data {
		if entry.Stage != "before_request" {
			t.Errorf("trace %s: stage %q leaked past an explicit filter", entry.TraceID, entry.Stage)
		}
	}
}

// TestLogsEchoesTheStageItApplied lets a client tell a filtered listing from the
// default one, which return overlapping but different sets.
func TestLogsEchoesTheStageItApplied(t *testing.T) {
	h, r := setupTestRouterWithLogs(stagedTraffic())
	adminKey := createAdminKey(t, h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodGet, "/admin/logs", "", adminKey))

	var payload struct {
		Filters struct {
			Stage  string   `json:"stage"`
			Stages []string `json:"stages"`
		} `json:"filters"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Filters.Stage != "" {
		t.Errorf("stage echo: want the empty request, got %q", payload.Filters.Stage)
	}
	if len(payload.Filters.Stages) != 2 {
		t.Errorf("stages echo: want the two terminal stages, got %v", payload.Filters.Stages)
	}
}
