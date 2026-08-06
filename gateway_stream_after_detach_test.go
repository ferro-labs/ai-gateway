package aigateway

import (
	"context"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/internal/requestlog"
	"github.com/ferro-labs/ai-gateway/plugin"
	pluginlogger "github.com/ferro-labs/ai-gateway/plugin/logger"
	"github.com/ferro-labs/ai-gateway/providers"
)

// cancelAwareWriter is a requestlog.Writer that refuses a write on a cancelled
// context, which is what every SQL-backed store does: database/sql returns the
// context's error from ExecContext rather than reaching the database. A writer
// that ignored the context could not tell a dropped row from a stored one.
type cancelAwareWriter struct {
	entries chan requestlog.Entry
}

func newCancelAwareWriter() *cancelAwareWriter {
	return &cancelAwareWriter{entries: make(chan requestlog.Entry, 8)}
}

func (w *cancelAwareWriter) Write(ctx context.Context, entry requestlog.Entry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	w.entries <- entry
	return nil
}

// The after_request stage of a stream must survive the client hanging up.
//
// It runs from the metering goroutine, which outlives the handler: the handler
// returns — cancelling the request context — as soon as it stops writing to the
// client, which for a client that disconnects at the end of a stream is while
// the after stage is still running. The durable row that stage writes is the
// only terminal row a completed stream produces, so a cancelled context there
// left the request in no operator-facing surface at all.
//
// The disconnect is staged from inside the stage rather than raced against it:
// the window is a few instructions wide in production, and a test that tried to
// hit it by timing would assert nothing on the runs it missed.
func TestRouteStream_AfterStageWritesDurableRowAfterClientDisconnect(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}

	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: mockProviderName, models: []string{"gpt-4o"}},
		streamFn: func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
			ch := make(chan providers.StreamChunk, 1)
			ch <- providers.StreamChunk{
				ID:     "chatcmpl-detach",
				Object: "chat.completion.chunk",
				Model:  "gpt-4o",
				Choices: []providers.StreamChoice{{
					Index:        0,
					Delta:        providers.MessageDelta{Role: "assistant", Content: "hi"},
					FinishReason: "stop",
				}},
			}
			close(ch)
			return ch, nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Stands in for the handler returning: the connection is gone by the time
	// the rest of the after stage runs. Registered first, so it runs first.
	disconnected := make(chan struct{})
	if err := gw.RegisterPlugin(plugin.StageAfterRequest, &testPlugin{
		name: "client-disconnect",
		typ:  plugin.TypeLogging,
		execFn: func(context.Context, *plugin.Context) error {
			cancel()
			close(disconnected)
			return nil
		},
	}); err != nil {
		t.Fatalf("register client-disconnect: %v", err)
	}

	writer := newCancelAwareWriter()
	reqLogger := &pluginlogger.RequestLogger{}
	reqLogger.SetRequestLogWriter(writer)
	if err := reqLogger.Init(map[string]any{"persist": true}); err != nil {
		t.Fatalf("init request-logger: %v", err)
	}
	if err := gw.RegisterPlugin(plugin.StageAfterRequest, reqLogger); err != nil {
		t.Fatalf("register request-logger: %v", err)
	}

	ch, err := gw.RouteStream(ctx, providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("RouteStream: %v", err)
	}
	drainStream(t, ch)

	select {
	case <-disconnected:
	case <-time.After(5 * time.Second):
		t.Fatal("the after_request stage never ran")
	}

	select {
	case entry := <-writer.entries:
		if entry.Stage != string(plugin.StageAfterRequest) {
			t.Fatalf("row stage = %q, want %q", entry.Stage, plugin.StageAfterRequest)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no durable after_request row was written; the stage was abandoned with the client's connection")
	}
}
