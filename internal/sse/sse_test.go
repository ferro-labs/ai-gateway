package sse

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/providers"
)

func TestWrite_WritesDoneSentinel(t *testing.T) {
	ch := make(chan providers.StreamChunk, 1)
	ch <- providers.StreamChunk{
		ID:    "stream-1",
		Model: "test-stream-model",
		Choices: []providers.StreamChoice{{
			Index: 0,
			Delta: providers.MessageDelta{Role: "assistant", Content: "hello"},
		}},
	}
	close(ch)

	w := httptest.NewRecorder()
	Write(context.Background(), w, ch)

	if !strings.HasSuffix(w.Body.String(), "data: [DONE]\n\n") {
		t.Fatalf("body should end with [DONE], got: %s", w.Body.String())
	}
}

func TestWrite_StopsWhenContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ch := make(chan providers.StreamChunk, 1)
	ch <- providers.StreamChunk{
		ID:    "stream-1",
		Model: "test-stream-model",
		Choices: []providers.StreamChoice{{
			Index: 0,
			Delta: providers.MessageDelta{Role: "assistant", Content: "hello"},
		}},
	}
	close(ch)

	w := httptest.NewRecorder()
	Write(ctx, w, ch)

	if w.Body.Len() != 0 {
		t.Fatalf("expected canceled stream to write nothing, got: %s", w.Body.String())
	}
}

func TestWriteAndFlush_SetsAndClearsDeadline(t *testing.T) {
	rw := newDeadlineRecorder()
	controller := http.NewResponseController(rw)
	bw := bufio.NewWriterSize(rw, 4096)

	err := writeAndFlush(context.Background(), controller, bw, func() error {
		_, writeErr := bw.WriteString("data: {}\n\n")
		return writeErr
	})
	if err != nil {
		t.Fatalf("writeAndFlush returned error: %v", err)
	}
	if len(rw.deadlines) < 2 {
		t.Fatalf("expected set and clear deadlines, got %d entries", len(rw.deadlines))
	}
	if rw.deadlines[0].IsZero() {
		t.Fatalf("first deadline should set a timeout")
	}
	if !rw.deadlines[len(rw.deadlines)-1].IsZero() {
		t.Fatalf("last deadline should clear timeout, got %v", rw.deadlines[len(rw.deadlines)-1])
	}
	if rw.flushes == 0 {
		t.Fatal("expected flush to be called")
	}
}

func TestWrite_TimesOutIdleStream(t *testing.T) {
	restore := SetIdleTimeoutForTest(10 * time.Millisecond)
	defer restore()

	ch := make(chan providers.StreamChunk)
	w := httptest.NewRecorder()

	Write(context.Background(), w, ch)

	body := w.Body.String()
	if !strings.Contains(body, `"code":"stream_timeout"`) {
		t.Fatalf("expected stream timeout payload, got: %s", body)
	}
	if strings.Contains(body, "data: [DONE]") {
		t.Fatalf("did not expect [DONE] after timeout, got: %s", body)
	}
}

// TestWrite_ResetsIdleTimeoutAfterChunk verifies a delivered chunk re-arms the
// idle timer so a following quiet gap does not emit a timeout. The idle timeout
// (500ms) dwarfs the ~10ms delivery gap, so the timer cannot fire in the
// dangerous direction and the test is deterministic.
func TestWrite_ResetsIdleTimeoutAfterChunk(t *testing.T) {
	restore := SetIdleTimeoutForTest(500 * time.Millisecond)
	defer restore()

	ch := make(chan providers.StreamChunk, 2)
	ch <- providers.StreamChunk{
		ID:    "stream-1",
		Model: "test-stream-model",
		Choices: []providers.StreamChoice{{
			Index: 0,
			Delta: providers.MessageDelta{Role: "assistant", Content: "hello"},
		}},
	}

	go func() {
		time.Sleep(10 * time.Millisecond)
		close(ch)
	}()

	w := httptest.NewRecorder()
	Write(context.Background(), w, ch)

	body := w.Body.String()
	if strings.Contains(body, `"code":"stream_timeout"`) {
		t.Fatalf("unexpected timeout payload, got: %s", body)
	}
	if !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("body should end with [DONE], got: %s", body)
	}
}

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
	flushes   int
}

func newDeadlineRecorder() *deadlineRecorder {
	return &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (r *deadlineRecorder) Flush() {
	r.flushes++
	r.ResponseRecorder.Flush()
}

func (r *deadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	r.deadlines = append(r.deadlines, deadline)
	return nil
}
