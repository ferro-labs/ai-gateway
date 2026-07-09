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
	copyBufferSize       = 32 * 1024
)

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
	clearErr := ClearWriteDeadline(w.controller)
	if err != nil {
		return n, err
	}
	if clearErr != nil {
		return n, clearErr
	}
	return n, nil
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
