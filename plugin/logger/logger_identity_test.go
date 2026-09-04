package logger

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/requestlog"
	"github.com/ferro-labs/ai-gateway/observability"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

// Every row the plugin writes names the end-user and session the request was
// made for, so GET /admin/logs can be read per user or per conversation. The
// identity comes from the request context, which is where the HTTP layer and
// the gateway put it; the plugin itself decides nothing about it.
func TestRequestLogger_RowsCarryRequestIdentity(t *testing.T) {
	store, err := requestlog.NewSQLiteWriter(t.Context(), filepath.Join(t.TempDir(), "requests.db"))
	if err != nil {
		t.Fatalf("new request log store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	l := &RequestLogger{}
	l.SetRequestLogWriter(store)
	if err := l.Init(map[string]any{"persist": true}); err != nil {
		t.Fatalf("init: %v", err)
	}

	ctx := logger.WithTraceID(context.Background(), "0123456789abcdef0123456789abcdef")
	ctx = observability.ContextWithRequestIdentity(ctx, observability.RequestIdentity{User: "user-42", SessionID: "sess-7"})

	pctx := plugin.NewContext(&providers.Request{Model: "gpt-4o"})
	t.Cleanup(func() { plugin.PutContext(pctx) })
	pctx.Metadata = map[string]any{}

	pctx.Stage = plugin.StageBeforeRequest
	if err := l.Execute(ctx, pctx); err != nil {
		t.Fatalf("before_request: %v", err)
	}
	pctx.Stage = plugin.StageAfterRequest
	pctx.Response = &providers.Response{Model: "gpt-4o", Provider: "openai"}
	pctx.Target = "openai"
	if err := l.Execute(ctx, pctx); err != nil {
		t.Fatalf("after_request: %v", err)
	}
	// A fresh request for the on_error row, so the terminal-row annotation path
	// does not swallow it.
	errCtx := logger.WithTraceID(ctx, "fedcba9876543210fedcba9876543210")
	epctx := plugin.NewContext(&providers.Request{Model: "gpt-4o"})
	t.Cleanup(func() { plugin.PutContext(epctx) })
	epctx.Metadata = map[string]any{}
	epctx.Stage = plugin.StageOnError
	epctx.Error = errors.New("upstream failed")
	epctx.Target = "openai"
	if err := l.Execute(errCtx, epctx); err != nil {
		t.Fatalf("on_error: %v", err)
	}

	listed, err := store.List(ctx, requestlog.Query{Limit: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed.Data) != 3 {
		t.Fatalf("rows = %d, want 3 (before_request, after_request, on_error)", len(listed.Data))
	}
	for _, row := range listed.Data {
		if row.UserID != "user-42" || row.SessionID != "sess-7" {
			t.Errorf("%s row = user %q session %q, want user-42 / sess-7", row.Stage, row.UserID, row.SessionID)
		}
	}
}
