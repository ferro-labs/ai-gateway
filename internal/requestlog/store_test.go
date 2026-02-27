package requestlog
package requestlog

import (
	"context"
	"path/filepath"
	"testing"

































































}	}		t.Fatal("expected error when before filter is missing")	if _, err := writer.Delete(ctx, MaintenanceQuery{}); err == nil {	writer := newSQLiteTestWriter(t)	ctx := context.Background()func TestSQLWriterDeleteRequiresBefore(t *testing.T) {}	}		t.Fatalf("expected 1 on_error entry after delete, total=%d returned=%d", afterDelete.Total, len(afterDelete.Data))	if afterDelete.Total != 1 || len(afterDelete.Data) != 1 {	}		t.Fatalf("list request logs after delete: %v", err)	if err != nil {	afterDelete, err := writer.List(ctx, Query{Limit: 10, Stage: "on_error"})	}		t.Fatalf("expected 1 deleted row, got %d", deleted)	if deleted != 1 {	}		t.Fatalf("delete request logs: %v", err)	if err != nil {	deleted, err := writer.Delete(ctx, MaintenanceQuery{Before: &before, Stage: "on_error"})	before := now.Add(-30 * time.Minute)	}		t.Fatalf("expected 2 on_error entries before delete, total=%d returned=%d", listed.Total, len(listed.Data))	if listed.Total != 2 || len(listed.Data) != 2 {	}		t.Fatalf("list request logs: %v", err)	if err != nil {	listed, err := writer.List(ctx, Query{Limit: 10, Stage: "on_error"})	}		}			t.Fatalf("write entry: %v", err)		if err := writer.Write(ctx, entry); err != nil {	for _, entry := range entries {	}		{TraceID: "3", Stage: "on_error", Provider: "openai", CreatedAt: now.Add(-10 * time.Minute)},		{TraceID: "2", Stage: "on_error", Provider: "openai", CreatedAt: now.Add(-2 * time.Hour)},		{TraceID: "1", Stage: "before_request", Provider: "openai", CreatedAt: now.Add(-3 * time.Hour)},	entries := []Entry{	now := time.Now().UTC()	writer := newSQLiteTestWriter(t)	ctx := context.Background()func TestSQLWriterListAndDelete(t *testing.T) {}	return writer	})		_ = writer.Close()	t.Cleanup(func() {	}		t.Fatalf("new sqlite writer: %v", err)	if err != nil {	writer, err := NewSQLiteWriter(filepath.Join(t.TempDir(), "request-logs.db"))	t.Helper()func newSQLiteTestWriter(t *testing.T) *SQLWriter {)	"time"