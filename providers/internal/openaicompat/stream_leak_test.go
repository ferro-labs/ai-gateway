package openaicompat

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// manyFrames builds an SSE body with n content deltas and no terminating
// [DONE], so a reader that is not being drained always has a decoded chunk
// ready and blocks on its channel send rather than on the source.
func manyFrames(n int) string {
	frame := `data: {"choices":[{"index":0,"delta":{"content":"tok"}}]}` + "\n\n"
	return strings.Repeat(frame, n)
}

// TestStreamSSE_ClientCancelMidStream_NoGoroutineLeak reproduces the direct-use
// failure mode for the shared OpenAI-compatible streaming path (~23 providers):
// a consumer reads one chunk mid-stream, cancels the request context, and stops
// reading. The reader is then blocked on `ch <- chunk` with more chunks pending.
// Before cancel-aware sends it blocked there forever; with the guard it observes
// the cancellation and exits. goleak.VerifyNone asserts no goroutine survives.
func TestStreamSSE_ClientCancelMidStream_NoGoroutineLeak(t *testing.T) {
	defer goleak.VerifyNone(t)

	ctx, cancel := context.WithCancel(context.Background())
	ch := StreamSSE(ctx, sseBody(manyFrames(100)))

	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("no first chunk within 2s")
	}

	// Client goes away: cancel and stop reading while chunks are still pending.
	cancel()
}

// TestStreamSSE_NaturalEOS_NoLeak is the inverse sanity check: a body that ends
// on its own must still close the channel and exit cleanly.
func TestStreamSSE_NaturalEOS_NoLeak(t *testing.T) {
	defer goleak.VerifyNone(t)

	body := manyFrames(3) + "data: [DONE]\n\n"
	ch := StreamSSE(context.Background(), sseBody(body))
	for range ch { //nolint:revive // draining to completion
	}
}
