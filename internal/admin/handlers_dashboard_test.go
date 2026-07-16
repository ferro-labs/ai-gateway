package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/requestlog"
)

func TestDashboardEndpoint(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeLogStore{entries: []requestlog.Entry{
		{TraceID: "1", Stage: "after_request", Provider: "openai", CreatedAt: now.Add(-2 * time.Minute)},
		{TraceID: "2", Stage: "on_error", Provider: "openai", CreatedAt: now.Add(-1 * time.Minute)},
	}}
	h, r := setupTestRouterWithLogs(store)
	adminKey := createAdminKey(t, h)

	expiredAt := now.Add(-10 * time.Minute)
	createTestKey(t, h, "expired-key", []string{ScopeReadOnly}, &expiredAt)
	active := createTestKey(t, h, "active-key", []string{ScopeReadOnly}, nil)
	validateTestKey(t, h, active.Key)

	req := authedRequest(http.MethodGet, "/admin/dashboard", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var payload struct {
		Providers struct {
			Total int `json:"total"`
		} `json:"providers"`
		Keys struct {
			Total      int   `json:"total"`
			Active     int   `json:"active"`
			Expired    int   `json:"expired"`
			TotalUsage int64 `json:"total_usage"`
		} `json:"keys"`
		RequestLogs struct {
			Enabled bool `json:"enabled"`
			Total   int  `json:"total"`
		} `json:"request_logs"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode dashboard payload: %v", err)
	}

	if payload.Providers.Total < 0 {
		t.Fatalf("invalid providers total: %d", payload.Providers.Total)
	}
	if payload.Keys.Total < 3 {
		t.Fatalf("expected at least 3 keys, got %d", payload.Keys.Total)
	}
	if payload.Keys.Expired < 1 {
		t.Fatalf("expected at least one expired key, got %d", payload.Keys.Expired)
	}
	if payload.Keys.TotalUsage < 1 {
		t.Fatalf("expected usage to be recorded, got %d", payload.Keys.TotalUsage)
	}
	if !payload.RequestLogs.Enabled {
		t.Fatal("expected request logs to be enabled")
	}
	if payload.RequestLogs.Total != 2 {
		t.Fatalf("expected request log total 2, got %d", payload.RequestLogs.Total)
	}
}

func TestDashboardEndpointWithoutLogs(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	req := authedRequest(http.MethodGet, "/admin/dashboard", "", adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var payload struct {
		RequestLogs struct {
			Enabled bool `json:"enabled"`
			Total   int  `json:"total"`
		} `json:"request_logs"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode dashboard payload: %v", err)
	}
	if payload.RequestLogs.Enabled {
		t.Fatal("expected request logs to be disabled")
	}
	if payload.RequestLogs.Total != 0 {
		t.Fatalf("expected request logs total 0, got %d", payload.RequestLogs.Total)
	}
}
