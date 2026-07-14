package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/providers/core"
	"go.uber.org/goleak"
)

// TestCompleteStream_ClientCancelMidStream_NoGoroutineLeak drives the real
// Anthropic provider against a server that bursts several SSE frames and then
// holds the connection open. The consumer reads one chunk, cancels the request
// context, and stops reading — leaving the producer blocked on `ch <- chunk`
// with more decoded chunks pending. Before cancel-aware sends it blocked there
// forever, leaking along with the upstream HTTP body. goleak.VerifyNone asserts
// no goroutine survives the cancellation.
func TestCompleteStream_ClientCancelMidStream_NoGoroutineLeak(t *testing.T) {
	defer goleak.VerifyNone(t)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("ResponseWriter is not a Flusher")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		writeFrame := func(s string) bool {
			if _, err := w.Write([]byte(s)); err != nil {
				return false
			}
			flusher.Flush()
			return true
		}

		if !writeFrame("event: message_start\n" +
			`data: {"type":"message_start","message":{"id":"msg_1","model":"claude","role":"assistant","usage":{"input_tokens":10}}}` + "\n\n") {
			return
		}
		// Burst several deltas so the producer has decoded chunks pending and is
		// blocked on a send (not on the socket) when the consumer stops reading.
		for range 8 {
			if !writeFrame("event: content_block_delta\n" +
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n") {
				return
			}
		}
		// Hold the stream open until the request is cancelled, mimicking a model
		// that has paused mid-response; the handler then returns so Close is fast.
		<-r.Context().Done()
	})

	srv := httptest.NewServer(handler)
	// Runs before the goleak defer (LIFO): the server's handler exits once the
	// request context is cancelled, so Close returns before verification.
	defer srv.Close()

	p, err := New("test-key", srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := p.CompleteStream(ctx, core.Request{
		Model:    "claude-3-5-sonnet",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err != nil {
		cancel()
		t.Fatalf("CompleteStream: %v", err)
	}

	// Require a genuine first chunk before cancelling: a closed channel or an
	// error chunk would mean the stream never really started, so accepting it
	// here would let the leak regression pass without exercising a live stream.
	select {
	case chunk, ok := <-ch:
		if !ok {
			cancel()
			t.Fatal("stream channel closed before any chunk")
		}
		if chunk.Error != nil {
			cancel()
			t.Fatalf("first chunk carried error: %v", chunk.Error)
		}
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("no first chunk within 2s")
	}

	// Client disconnects: cancel and stop reading. The producer must abandon its
	// pending send, close the body, and exit.
	cancel()
}
