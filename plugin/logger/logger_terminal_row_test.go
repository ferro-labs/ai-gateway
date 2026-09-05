package logger

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/requestlog"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

// A request can complete and then fail: the provider answers, this plugin
// writes its after_request row, and an after_request plugin ordered behind it
// breaks — which sends the pipeline through on_error with a terminal row
// already in the store. Writing a second one there gives one request two
// terminal rows, which the default /admin/logs view lists twice and every
// /admin/logs/stats figure counts twice, in the middle of the failure the
// operator is reading them to understand.

// completedThenFailed drives one request's after_request and on_error stages
// through a single plugin instance, exactly as the pipeline does.
func completedThenFailed(t *testing.T, writer requestlog.Writer) (context.Context, *plugin.Context) {
	t.Helper()

	l := &RequestLogger{}
	l.SetRequestLogWriter(writer)
	if err := l.Init(map[string]any{"persist": true}); err != nil {
		t.Fatalf("init: %v", err)
	}

	ctx := logger.WithTraceID(context.Background(), "01960000000000000000000001")
	pctx := plugin.NewContext(&providers.Request{Model: "gpt-4"})
	t.Cleanup(func() { plugin.PutContext(pctx) })
	pctx.Metadata = map[string]any{}
	pctx.Response = &providers.Response{
		Model:    "gpt-4",
		Provider: "openai",
		Usage:    providers.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}
	pctx.Target = "openai"

	pctx.Stage = plugin.StageAfterRequest
	if err := l.Execute(ctx, pctx); err != nil {
		t.Fatalf("after_request: %v", err)
	}

	pctx.Stage = plugin.StageOnError
	pctx.Error = errors.New("budget plugin backend unavailable")
	if err := l.Execute(ctx, pctx); err != nil {
		t.Fatalf("on_error: %v", err)
	}
	return ctx, pctx
}

func TestRequestLogger_CompletedThenFailedWritesOneTerminalRow(t *testing.T) {
	store, err := requestlog.NewSQLiteWriter(t.Context(), filepath.Join(t.TempDir(), "requests.db"))
	if err != nil {
		t.Fatalf("new request log store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close request log store: %v", err)
		}
	})

	ctx, _ := completedThenFailed(t, store)

	listed, err := store.List(ctx, requestlog.Query{Stages: requestlog.TerminalStages()})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if listed.Total != 1 {
		stages := make([]string, 0, len(listed.Data))
		for _, e := range listed.Data {
			stages = append(stages, e.Stage)
		}
		t.Fatalf("one request produced %d terminal rows (%v), want 1", listed.Total, stages)
	}

	row := listed.Data[0]
	if row.Stage != "after_request" {
		t.Errorf("surviving row stage = %q, want after_request", row.Stage)
	}
	// Suppressing the second row must not lose the failure with it, or the
	// request would be recorded as a success.
	if row.ErrorMessage == "" {
		t.Error("the surviving row does not name the failure, so a failed request reads as a success")
	}
	// What the provider actually did still has to be on the row.
	if row.TotalTokens != 15 || row.Provider != "openai" {
		t.Errorf("surviving row lost the completed call's facts: %+v", row)
	}

	stats, err := store.Stats(ctx, requestlog.Query{})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.TotalEntries != 1 {
		t.Errorf("stats counted %d requests, want 1", stats.TotalEntries)
	}
	if stats.ErrorEntries != 1 {
		t.Errorf("stats counted %d errors, want 1", stats.ErrorEntries)
	}
}

// A writer that cannot annotate keeps the old behaviour. Losing the failure
// entirely would be worse than recording it twice.
func TestRequestLogger_CompletedThenFailedStillRecordsWithoutAnAnnotator(t *testing.T) {
	writer := &recordingWriter{}
	if _, ok := any(writer).(requestlog.ErrorAnnotator); ok {
		t.Fatal("this test needs a writer that cannot annotate")
	}

	completedThenFailed(t, writer)

	if len(writer.entries) != 2 {
		t.Fatalf("wrote %d rows, want 2 (the failure must still be recorded)", len(writer.entries))
	}
	if writer.entries[1].Stage != string(plugin.StageOnError) {
		t.Errorf("second row stage = %q, want on_error", writer.entries[1].Stage)
	}
}

// A blank trace id matches nothing rather than every unattributed row, which is
// what an unguarded `trace_id = ”` would rewrite.
func TestSQLWriter_AnnotateErrorIgnoresABlankTraceID(t *testing.T) {
	store, err := requestlog.NewSQLiteWriter(t.Context(), filepath.Join(t.TempDir(), "requests.db"))
	if err != nil {
		t.Fatalf("new request log store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close request log store: %v", err)
		}
	})

	ctx := context.Background()
	if err := store.Write(ctx, requestlog.Entry{Stage: "after_request", Model: "gpt-4"}); err != nil {
		t.Fatalf("write: %v", err)
	}

	annotated, err := store.AnnotateError(ctx, "", "after_request", "boom")
	if err != nil {
		t.Fatalf("annotate: %v", err)
	}
	if annotated {
		t.Fatal("a blank trace id claimed a row")
	}

	listed, err := store.List(ctx, requestlog.Query{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if listed.Data[0].ErrorMessage != "" {
		t.Errorf("an unrelated row was annotated: %q", listed.Data[0].ErrorMessage)
	}
}
