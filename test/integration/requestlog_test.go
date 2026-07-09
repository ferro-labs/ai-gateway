//go:build integration
// +build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/requestlog"
)

func TestPostgresRequestLog_WriteAndList(t *testing.T) {
	w, err := requestlog.NewPostgresWriter(t.Context(), testDSN)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	t.Cleanup(func() { truncateTable(t, "request_logs"); _ = w.Close() })

	ctx := context.Background()
	entry := requestlog.Entry{
		TraceID:          "trace-1",
		Stage:            "after_request",
		Model:            "gpt-4o-mini",
		Provider:         "openai",
		PromptTokens:     7,
		CompletionTokens: 9,
		TotalTokens:      16,
		CreatedAt:        time.Now().UTC(),
	}
	if err := w.Write(ctx, entry); err != nil {
		t.Fatalf("write: %v", err)
	}

	result, err := w.List(ctx, requestlog.Query{Limit: 10, Provider: "openai"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if result.Total != 1 || len(result.Data) != 1 {
		t.Fatalf("expected 1 log, total=%d len=%d", result.Total, len(result.Data))
	}
	if result.Data[0].TraceID != "trace-1" {
		t.Fatalf("unexpected trace id: %s", result.Data[0].TraceID)
	}
}

func TestPostgresRequestLog_Pagination(t *testing.T) {
	w, err := requestlog.NewPostgresWriter(t.Context(), testDSN)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	t.Cleanup(func() { truncateTable(t, "request_logs"); _ = w.Close() })

	ctx := context.Background()
	for i := range 5 {
		entry := requestlog.Entry{
			TraceID:   "page-" + string(rune('a'+i)),
			Stage:     "after_request",
			Model:     "gpt-4o-mini",
			Provider:  "openai",
			CreatedAt: time.Now().UTC().Add(time.Duration(i) * time.Second),
		}
		if err := w.Write(ctx, entry); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	page1, err := w.List(ctx, requestlog.Query{Limit: 2, Offset: 0})
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if page1.Total != 5 {
		t.Fatalf("expected total=5, got %d", page1.Total)
	}
	if len(page1.Data) != 2 {
		t.Fatalf("expected 2 on page 1, got %d", len(page1.Data))
	}

	page3, err := w.List(ctx, requestlog.Query{Limit: 2, Offset: 4})
	if err != nil {
		t.Fatalf("page 3: %v", err)
	}
	if len(page3.Data) != 1 {
		t.Fatalf("expected 1 on last page, got %d", len(page3.Data))
	}
}

func TestPostgresRequestLog_Delete(t *testing.T) {
	w, err := requestlog.NewPostgresWriter(t.Context(), testDSN)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	t.Cleanup(func() { truncateTable(t, "request_logs"); _ = w.Close() })

	ctx := context.Background()
	now := time.Now().UTC()

	old := requestlog.Entry{TraceID: "old", Stage: "after_request", Model: "m", Provider: "p", CreatedAt: now.Add(-2 * time.Hour)}
	recent := requestlog.Entry{TraceID: "recent", Stage: "after_request", Model: "m", Provider: "p", CreatedAt: now}

	if err := w.Write(ctx, old); err != nil {
		t.Fatalf("write old: %v", err)
	}
	if err := w.Write(ctx, recent); err != nil {
		t.Fatalf("write recent: %v", err)
	}

	cutoff := now.Add(-1 * time.Hour)
	deleted, err := w.Delete(ctx, requestlog.MaintenanceQuery{Before: &cutoff})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 deleted, got %d", deleted)
	}

	result, err := w.List(ctx, requestlog.Query{Limit: 10})
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if result.Total != 1 || result.Data[0].TraceID != "recent" {
		t.Fatalf("expected only 'recent' to remain, got total=%d", result.Total)
	}
}
