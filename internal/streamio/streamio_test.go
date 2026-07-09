package streamio

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWriteAndFlushSetsAndClearsDeadline(t *testing.T) {
	rw := newDeadlineRecorder()
	controller := http.NewResponseController(rw)

	err := WriteAndFlush(context.Background(), controller, nil, func() error {
		_, writeErr := rw.Write([]byte("data: {}\n\n"))
		return writeErr
	})
	if err != nil {
		t.Fatalf("WriteAndFlush returned error: %v", err)
	}
	if len(rw.deadlines) < 2 {
		t.Fatalf("expected set and clear deadlines, got %d entries", len(rw.deadlines))
	}
	if rw.deadlines[0].IsZero() {
		t.Fatal("first deadline should set a timeout")
	}
	if !rw.deadlines[len(rw.deadlines)-1].IsZero() {
		t.Fatalf("last deadline should clear timeout, got %v", rw.deadlines[len(rw.deadlines)-1])
	}
	if rw.flushes == 0 {
		t.Fatal("expected response flush")
	}
}

func TestWriteAndFlushIgnoresUnsupportedDeadline(t *testing.T) {
	rw := httptest.NewRecorder()
	controller := http.NewResponseController(rw)

	err := WriteAndFlush(context.Background(), controller, nil, func() error {
		_, writeErr := rw.Write([]byte("ok"))
		return writeErr
	})
	if err != nil {
		t.Fatalf("WriteAndFlush returned error for unsupported deadline: %v", err)
	}
	if rw.Body.String() != "ok" {
		t.Fatalf("body = %q, want ok", rw.Body.String())
	}
}

func TestWriteAndFlushPropagatesDeadlineError(t *testing.T) {
	wantErr := errors.New("deadline failed")
	rw := newDeadlineRecorder()
	rw.deadlineErr = wantErr
	controller := http.NewResponseController(rw)

	err := WriteAndFlush(context.Background(), controller, nil, func() error {
		t.Fatal("writeFn should not run when setting deadline fails")
		return nil
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestCopyWritesWithDeadlinesAndFlushes(t *testing.T) {
	rw := newDeadlineRecorder()
	written, err := Copy(context.Background(), rw, strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("Copy returned error: %v", err)
	}
	if written != 5 {
		t.Fatalf("written = %d, want 5", written)
	}
	if rw.Body.String() != "hello" {
		t.Fatalf("body = %q, want hello", rw.Body.String())
	}
	if len(rw.deadlines) < 2 {
		t.Fatalf("expected set and clear deadlines, got %d entries", len(rw.deadlines))
	}
	if rw.flushes == 0 {
		t.Fatal("expected response flush")
	}
}

func TestCopyReturnsCanceledContextBeforeWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rw := newDeadlineRecorder()
	written, err := Copy(ctx, rw, strings.NewReader("hello"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if written != 0 {
		t.Fatalf("written = %d, want 0", written)
	}
	if rw.Body.Len() != 0 {
		t.Fatalf("body should be empty, got %q", rw.Body.String())
	}
}

func TestNewIdleReadCloser_FiresWhenUpstreamStalls(t *testing.T) {
	fired := make(chan struct{})
	rc := NewIdleReadCloser(io.NopCloser(blockingReader{}), 10*time.Millisecond, func() { close(fired) })
	defer func() { _ = rc.Close() }()

	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("idle callback never fired while the upstream stalled")
	}
}

func TestNewIdleReadCloser_CloseStopsTimer(t *testing.T) {
	var fired atomic.Bool
	rc := NewIdleReadCloser(io.NopCloser(blockingReader{}), 10*time.Millisecond, func() { fired.Store(true) })
	if err := rc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	if fired.Load() {
		t.Fatal("idle callback fired after Close")
	}
}

func TestNewIdleReadCloser_ReadRearmsTimer(t *testing.T) {
	var fired atomic.Bool
	src := io.NopCloser(strings.NewReader("abcdef"))
	rc := NewIdleReadCloser(src, 40*time.Millisecond, func() { fired.Store(true) })
	defer func() { _ = rc.Close() }()

	// Six reads spaced under the bound must never trip it, even though their
	// total duration exceeds it.
	buf := make([]byte, 1)
	for range 6 {
		if _, err := rc.Read(buf); err != nil {
			t.Fatalf("Read: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
		if fired.Load() {
			t.Fatal("idle callback fired while reads were still completing")
		}
	}
}

func TestNewIdleReadCloser_NonPositiveIdleDisablesBound(t *testing.T) {
	src := io.NopCloser(strings.NewReader("x"))
	if got := NewIdleReadCloser(src, 0, func() { t.Fatal("must not fire") }); got != src {
		t.Fatalf("zero idle should return the source unchanged, got %T", got)
	}
}

func TestWrapResponseWriter_SetsAndClearsDeadlinePerWrite(t *testing.T) {
	rw := newDeadlineRecorder()
	w := WrapResponseWriter(rw)

	for range 2 {
		if _, err := w.Write([]byte("chunk")); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	// Each write must set a deadline then clear it, so the gap between writes is
	// unbounded by http.Server's WriteTimeout.
	if len(rw.deadlines) != 4 {
		t.Fatalf("deadlines = %v, want set/clear per write (4 entries)", rw.deadlines)
	}
	for i, d := range rw.deadlines {
		if wantZero := i%2 == 1; d.IsZero() != wantZero {
			t.Fatalf("deadlines[%d].IsZero() = %v, want %v", i, d.IsZero(), wantZero)
		}
	}
}

func TestWrapResponseWriter_DeadlineFailureAbortsWrite(t *testing.T) {
	wantErr := errors.New("deadline failed")
	rw := newDeadlineRecorder()
	rw.deadlineErr = wantErr
	w := WrapResponseWriter(rw)

	n, err := w.Write([]byte("chunk"))
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if n != 0 {
		t.Fatalf("written = %d, want 0", n)
	}
	if rw.Body.Len() != 0 {
		t.Fatalf("body should be empty, got %q", rw.Body.String())
	}
}

// The wrapper embeds the http.ResponseWriter interface, so it does not itself
// satisfy http.Flusher. ReverseProxy reaches Flush through ResponseController,
// which walks Unwrap — this pins that contract.
func TestWrapResponseWriter_UnwrapExposesFlusher(t *testing.T) {
	rw := newDeadlineRecorder()
	w := WrapResponseWriter(rw)

	if _, ok := w.(http.Flusher); ok {
		t.Fatal("wrapper unexpectedly satisfies http.Flusher directly; test no longer covers the Unwrap path")
	}
	if err := http.NewResponseController(w).Flush(); err != nil {
		t.Fatalf("Flush through ResponseController: %v", err)
	}
	if rw.flushes != 1 {
		t.Fatalf("flushes = %d, want 1", rw.flushes)
	}
}

type blockingReader struct{}

func (blockingReader) Read([]byte) (int, error) {
	select {} // never returns; stands in for a stalled upstream
}

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines   []time.Time
	deadlineErr error
	flushes     int
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
	return r.deadlineErr
}
