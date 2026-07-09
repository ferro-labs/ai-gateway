// Package streamio contains shared helpers for bounded streaming response writes.
package streamio

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"
)

const (
	// DefaultWriteDeadline bounds each client-facing streaming write.
	DefaultWriteDeadline = 15 * time.Second

	copyBufferSize = 32 * 1024
)

// idleTimeout bounds the gap between two upstream reads. Clearing the write
// deadline after every write also clears http.Server's WriteTimeout for the
// connection, so without this a trickling upstream would hold a goroutine and a
// connection open indefinitely. Generous enough for a reasoning model's
// time-to-first-chunk; every real provider heartbeats well inside it.
var idleTimeout = 5 * time.Minute

// IdleTimeout returns the active upstream idle bound.
func IdleTimeout() time.Duration { return idleTimeout }

// SetIdleTimeoutForTest overrides the idle bound and returns a restore function.
func SetIdleTimeoutForTest(d time.Duration) func() {
	prev := idleTimeout
	idleTimeout = d
	return func() { idleTimeout = prev }
}

// NewIdleReadCloser wraps rc so onIdle fires when no read completes within idle.
// Pass the CancelFunc of the context driving the upstream request: cancelling it
// unblocks the in-flight Read instead of leaving it parked forever. Closing the
// returned ReadCloser stops the timer and closes rc. A non-positive idle
// disables the bound and returns rc unchanged.
func NewIdleReadCloser(rc io.ReadCloser, idle time.Duration, onIdle func()) io.ReadCloser {
	if idle <= 0 {
		return rc
	}
	return &idleReadCloser{rc: rc, idle: idle, timer: time.AfterFunc(idle, onIdle)}
}

type idleReadCloser struct {
	rc    io.ReadCloser
	timer *time.Timer
	idle  time.Duration
}

// Read re-arms the timer after each read returns, so the timer that is running
// while Read blocks is always the one armed by the previous read.
func (r *idleReadCloser) Read(p []byte) (int, error) {
	n, err := r.rc.Read(p)
	r.timer.Reset(r.idle)
	return n, err
}

func (r *idleReadCloser) Close() error {
	r.timer.Stop()
	return r.rc.Close()
}

// WrapResponseWriter returns a ResponseWriter that sets a short write deadline
// around every Write call. Unsupported deadline operations are ignored.
func WrapResponseWriter(w http.ResponseWriter) http.ResponseWriter {
	return &deadlineResponseWriter{
		ResponseWriter: w,
		controller:     http.NewResponseController(w),
		timeout:        DefaultWriteDeadline,
	}
}

type deadlineResponseWriter struct {
	http.ResponseWriter
	controller *http.ResponseController
	timeout    time.Duration
}

func (w *deadlineResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *deadlineResponseWriter) Write(p []byte) (int, error) {
	if err := SetWriteDeadline(w.controller, time.Now().Add(w.timeout)); err != nil {
		return 0, err
	}
	n, err := w.ResponseWriter.Write(p)
	// The write already happened; a failure to clear the deadline is not a write
	// error. Clearing it matters so the gap until the next write stays unbounded
	// by http.Server's WriteTimeout — the idle bound is the reader's job.
	_ = ClearWriteDeadline(w.controller)
	return n, err
}

// Copy streams src to w, applying the default write deadline to each write and
// flushing after every chunk.
func Copy(ctx context.Context, w http.ResponseWriter, src io.Reader) (int64, error) {
	controller := http.NewResponseController(w)
	buf := make([]byte, copyBufferSize)
	var written int64

	for {
		select {
		case <-ctx.Done():
			return written, ctx.Err()
		default:
		}

		nr, readErr := src.Read(buf)
		if nr > 0 {
			chunk := buf[:nr]
			if err := WriteAndFlush(ctx, controller, nil, func() error {
				nw, writeErr := w.Write(chunk)
				if nw > 0 {
					written += int64(nw)
				}
				if writeErr != nil {
					return writeErr
				}
				if nw != nr {
					return io.ErrShortWrite
				}
				return nil
			}); err != nil {
				return written, err
			}
		}

		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return written, nil
			}
			return written, readErr
		}
	}
}

// WriteAndFlush sets the default write deadline, runs writeFn, flushes the
// optional buffered writer, flushes the response, then clears the deadline.
func WriteAndFlush(ctx context.Context, controller *http.ResponseController, flushBuffer func() error, writeFn func() error) error {
	if err := SetWriteDeadline(controller, time.Now().Add(DefaultWriteDeadline)); err != nil {
		return err
	}
	defer func() {
		_ = ClearWriteDeadline(controller)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	if err := writeFn(); err != nil {
		return err
	}
	if flushBuffer != nil {
		if err := flushBuffer(); err != nil {
			return err
		}
	}
	return Flush(controller)
}

// Flush flushes the response when supported.
func Flush(controller *http.ResponseController) error {
	if err := controller.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}

// SetWriteDeadline applies deadline when supported.
func SetWriteDeadline(controller *http.ResponseController, deadline time.Time) error {
	if err := controller.SetWriteDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}

// ClearWriteDeadline removes any active write deadline when supported.
func ClearWriteDeadline(controller *http.ResponseController) error {
	return SetWriteDeadline(controller, time.Time{})
}
