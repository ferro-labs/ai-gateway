package streamio

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
