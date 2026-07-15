package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/requestlog"
)

func TestLogsEndpoint(t *testing.T) {
	now := time.Now().UTC()
	reader := &fakeLogReader{entries: []requestlog.Entry{
		{TraceID: "1", Stage: "after_request", Model: "gpt-4", Provider: "openai", TotalTokens: 10, CreatedAt: now.Add(-2 * time.Minute)},
		{TraceID: "2", Stage: "on_error", Model: "gpt-4", Provider: "openai", ErrorMessage: "boom", CreatedAt: now.Add(-1 * time.Minute)},
	}}
	h, r := setupTestRouterWithLogs(reader)
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodGet, "/admin/logs?stage=on_error&limit=10", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var payload struct {
		Data    []requestlog.Entry `json:"data"`
		Summary struct {
			TotalEntries    int `json:"total_entries"`
			ReturnedEntries int `json:"returned_entries"`
		} `json:"summary"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode logs response: %v", err)
	}
	if payload.Summary.TotalEntries != 1 || payload.Summary.ReturnedEntries != 1 {
		t.Fatalf("unexpected summary: %+v", payload.Summary)
	}
	if len(payload.Data) != 1 || payload.Data[0].Stage != "on_error" {
		t.Fatalf("expected filtered on_error entry")
	}
}

func TestLogsEndpointUsesSnakeCaseFields(t *testing.T) {
	now := time.Now().UTC()
	reader := &fakeLogReader{entries: []requestlog.Entry{
		{
			TraceID:          "trace-1",
			Stage:            "after_request",
			Model:            "gpt-4",
			Provider:         "openai",
			PromptTokens:     12,
			CompletionTokens: 34,
			TotalTokens:      46,
			ErrorMessage:     "boom",
			CreatedAt:        now,
		},
	}}
	h, r := setupTestRouterWithLogs(reader)
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodGet, "/admin/logs?limit=1", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var payload struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode logs response: %v", err)
	}
	if len(payload.Data) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(payload.Data))
	}

	entry := payload.Data[0]
	for _, key := range []string{
		"trace_id",
		"stage",
		"model",
		"provider",
		"prompt_tokens",
		"completion_tokens",
		"total_tokens",
		"error_message",
		"created_at",
	} {
		if _, ok := entry[key]; !ok {
			t.Fatalf("expected JSON field %q in response entry: %+v", key, entry)
		}
	}

	for _, key := range []string{"TraceID", "Stage", "Model", "Provider", "CreatedAt", "ErrorMessage"} {
		if _, ok := entry[key]; ok {
			t.Fatalf("did not expect Go-style JSON field %q in response entry: %+v", key, entry)
		}
	}
}
