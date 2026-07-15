package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/requestlog"
)

func TestLogsEndpointNotEnabled(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodGet, "/admin/logs", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", w.Code)
	}
}

func TestLogsEndpointInvalidSince(t *testing.T) {
	reader := &fakeLogReader{entries: []requestlog.Entry{}}
	h, r := setupTestRouterWithLogs(reader)
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodGet, "/admin/logs?since=bad", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestLogsStatsEndpoint(t *testing.T) {
	reader := &fakeLogReader{stats: requestlog.StatsResult{
		TotalEntries: 3,
		ErrorEntries: 1,
		TotalTokens:  35,
		ByStage:      map[string]int{"after_request": 2, "on_error": 1},
		ByProvider:   map[string]int{"openai": 2, "anthropic": 1},
		ByModel:      map[string]int{"gpt-4": 2, "claude": 1},
	}}
	h, r := setupTestRouterWithLogs(reader)
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodGet, "/admin/logs/stats", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var payload struct {
		Summary struct {
			TotalEntries int `json:"total_entries"`
			ErrorEntries int `json:"error_entries"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"summary"`
		ByStage    map[string]int `json:"by_stage"`
		ByProvider map[string]int `json:"by_provider"`
		ByModel    map[string]int `json:"by_model"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode logs stats response: %v", err)
	}
	if payload.Summary.TotalEntries != 3 {
		t.Fatalf("expected total_entries=3, got %d", payload.Summary.TotalEntries)
	}
	if payload.Summary.ErrorEntries != 1 {
		t.Fatalf("expected error_entries=1, got %d", payload.Summary.ErrorEntries)
	}
	if payload.Summary.TotalTokens != 35 {
		t.Fatalf("expected total_tokens=35, got %d", payload.Summary.TotalTokens)
	}
	if payload.ByStage["after_request"] != 2 || payload.ByStage["on_error"] != 1 {
		t.Fatalf("unexpected by_stage: %+v", payload.ByStage)
	}
	if payload.ByProvider["openai"] != 2 || payload.ByProvider["anthropic"] != 1 {
		t.Fatalf("unexpected by_provider: %+v", payload.ByProvider)
	}
	if payload.ByModel["gpt-4"] != 2 || payload.ByModel["claude"] != 1 {
		t.Fatalf("unexpected by_model: %+v", payload.ByModel)
	}
}

func TestLogsStatsEndpointWithLimit(t *testing.T) {
	reader := &fakeLogReader{stats: requestlog.StatsResult{
		TotalEntries: 3,
		ByStage:      map[string]int{"after_request": 2, "on_error": 1},
		ByProvider:   map[string]int{"openai": 2, "anthropic": 1},
		ByModel:      map[string]int{"gpt-4": 2, "claude": 1},
	}}
	h, r := setupTestRouterWithLogs(reader)
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodGet, "/admin/logs/stats?limit=1", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var payload struct {
		Summary struct {
			TotalEntries int `json:"total_entries"`
		} `json:"summary"`
		ByProvider map[string]int `json:"by_provider"`
		ByModel    map[string]int `json:"by_model"`
		Filters    struct {
			Limit int `json:"limit"`
		} `json:"filters"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode logs stats response: %v", err)
	}
	if payload.Summary.TotalEntries != 3 {
		t.Fatalf("expected total_entries=3, got %d", payload.Summary.TotalEntries)
	}
	if payload.Filters.Limit != 1 {
		t.Fatalf("expected filters.limit=1, got %d", payload.Filters.Limit)
	}
	if len(payload.ByProvider) != 1 || payload.ByProvider["openai"] != 2 {
		t.Fatalf("unexpected limited by_provider: %+v", payload.ByProvider)
	}
	if len(payload.ByModel) != 1 || payload.ByModel["gpt-4"] != 2 {
		t.Fatalf("unexpected limited by_model: %+v", payload.ByModel)
	}
}

func TestLogsStatsEndpointReturnsExactCounts(t *testing.T) {
	// Stats is now an exact SQL aggregation: the summary reports the true total
	// and the scan-cap fields are gone.
	reader := &fakeLogReader{stats: requestlog.StatsResult{
		TotalEntries: 5010,
		ErrorEntries: 7,
		TotalTokens:  5010,
		ByStage:      map[string]int{"after_request": 5010},
		ByProvider:   map[string]int{"openai": 5010},
		ByModel:      map[string]int{"gpt-4": 5010},
	}}
	h, r := setupTestRouterWithLogs(reader)
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodGet, "/admin/logs/stats", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var payload struct {
		Summary map[string]any `json:"summary"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode logs stats response: %v", err)
	}

	if got := payload.Summary["total_entries"]; got != float64(5010) {
		t.Fatalf("expected total_entries=5010, got %v", got)
	}
	if got := payload.Summary["error_entries"]; got != float64(7) {
		t.Fatalf("expected error_entries=7, got %v", got)
	}
	if got := payload.Summary["total_tokens"]; got != float64(5010) {
		t.Fatalf("expected total_tokens=5010, got %v", got)
	}
	for _, removed := range []string{"truncated", "available_entries", "scan_limit"} {
		if _, ok := payload.Summary[removed]; ok {
			t.Fatalf("expected summary key %q to be removed", removed)
		}
	}
}

func TestLogsStatsEndpointInvalidLimit(t *testing.T) {
	reader := &fakeLogReader{entries: []requestlog.Entry{}}
	h, r := setupTestRouterWithLogs(reader)
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodGet, "/admin/logs/stats?limit=bad", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestLogsStatsEndpointInvalidSince(t *testing.T) {
	reader := &fakeLogReader{entries: []requestlog.Entry{}}
	h, r := setupTestRouterWithLogs(reader)
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodGet, "/admin/logs/stats?since=bad", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestLogsStatsEndpointNotEnabled(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodGet, "/admin/logs/stats", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", w.Code)
	}
}

func TestDeleteLogsEndpoint(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeLogStore{entries: []requestlog.Entry{
		{TraceID: "1", Stage: "on_error", Provider: "openai", CreatedAt: now.Add(-2 * time.Hour)},
		{TraceID: "2", Stage: "after_request", Provider: "openai", CreatedAt: now.Add(-90 * time.Minute)},
		{TraceID: "3", Stage: "on_error", Provider: "openai", CreatedAt: now.Add(-10 * time.Minute)},
	}}
	h, r := setupTestRouterWithLogs(store)
	adminKey := createAdminKey(t, h)

	before := now.Add(-30 * time.Minute).Format(time.RFC3339)
	req := authedRequest(http.MethodDelete, "/admin/logs?before="+before+"&stage=on_error", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var payload struct {
		Deleted int `json:"deleted"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode delete logs response: %v", err)
	}
	if payload.Deleted != 1 {
		t.Fatalf("expected deleted=1, got %d", payload.Deleted)
	}

	listReq := authedRequest(http.MethodGet, "/admin/logs?stage=on_error", "", adminKey)
	listW := httptest.NewRecorder()
	r.ServeHTTP(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("expected list 200, got %d: %s", listW.Code, listW.Body.String())
	}

	var listPayload struct {
		Summary struct {
			TotalEntries int `json:"total_entries"`
		} `json:"summary"`
	}
	if err := json.NewDecoder(listW.Body).Decode(&listPayload); err != nil {
		t.Fatalf("decode list logs response: %v", err)
	}
	if listPayload.Summary.TotalEntries != 1 {
		t.Fatalf("expected one on_error entry after cleanup, got %d", listPayload.Summary.TotalEntries)
	}
}

func TestDeleteLogsEndpointMissingBefore(t *testing.T) {
	store := &fakeLogStore{entries: []requestlog.Entry{}}
	h, r := setupTestRouterWithLogs(store)
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodDelete, "/admin/logs", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestDeleteLogsEndpointInvalidBefore(t *testing.T) {
	store := &fakeLogStore{entries: []requestlog.Entry{}}
	h, r := setupTestRouterWithLogs(store)
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodDelete, "/admin/logs?before=bad", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestDeleteLogsEndpointNotEnabled(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodDelete, "/admin/logs?before=2026-02-01T00:00:00Z", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", w.Code)
	}
}
