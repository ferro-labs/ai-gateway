// Package sse provides Server-Sent Events streaming for OpenAI-compatible responses.
package sse

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/ferro-labs/ai-gateway/providers"
)

var (
	writeDeadline = 15 * time.Second
	idleTimeout   = 2 * time.Minute
)

// SetIdleTimeoutForTest overrides the idle timeout for testing and returns a restore function.
func SetIdleTimeoutForTest(d time.Duration) func() {
	prev := idleTimeout
	idleTimeout = d
	return func() { idleTimeout = prev }
}

// Write streams SSE chunks from ch to the response writer.
func Write(ctx context.Context, w http.ResponseWriter, ch <-chan providers.StreamChunk) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	controller := http.NewResponseController(w)
	_ = clearWriteDeadline(controller)

	bw := bufio.NewWriterSize(w, 4096)
	enc := json.NewEncoder(bw)
	now := time.Now().Unix()
	idleTimer := time.NewTimer(idleTimeout)
	defer idleTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			logging.FromContext(ctx).Debug("stream response canceled", "error", ctx.Err())
			return
		case <-idleTimer.C:
			logging.FromContext(ctx).Warn("stream response timed out waiting for next chunk", "idle_timeout_ms", idleTimeout.Milliseconds())
			_ = writeAndFlush(ctx, controller, bw, func() error {
				return writeEvent(bw, enc, map[string]any{
					"error": map[string]string{
						"message": "stream timed out waiting for next chunk",
						"type":    "timeout_error",
						"code":    "stream_timeout",
					},
				})
			})
			return
		case chunk, ok := <-ch:
			if !ok {
				_ = writeAndFlush(ctx, controller, bw, func() error {
					_, err := bw.WriteString("data: [DONE]\n\n")
					return err
				})
				return
			}

			if chunk.Error != nil {
				_ = writeAndFlush(ctx, controller, bw, func() error {
					return writeEvent(bw, enc, map[string]any{
						"error": map[string]string{
							"message": chunk.Error.Error(),
							"type":    "stream_error",
							"code":    "stream_error",
						},
					})
				})
				return
			}
			if chunk.Object == "" {
				chunk.Object = "chat.completion.chunk"
			}
			if chunk.Created == 0 {
				chunk.Created = now
			}
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(idleTimeout)

			if err := writeAndFlush(ctx, controller, bw, func() error {
				return writeChunk(bw, enc, &chunk)
			}); err != nil {
				if !errors.Is(err, context.Canceled) {
					logging.FromContext(ctx).Debug("stream response write failed", "error", err)
				}
				return
			}
		}
	}
}

func writeAndFlush(ctx context.Context, controller *http.ResponseController, bw *bufio.Writer, writeFn func() error) error {
	if err := setWriteDeadline(controller, time.Now().Add(writeDeadline)); err != nil {
		return err
	}
	defer func() {
		_ = clearWriteDeadline(controller)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	if err := writeFn(); err != nil {
		return err
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	return flush(controller)
}

// writeChunk writes a single stream chunk as an SSE event using a
// concrete-typed argument. This is the highest-frequency write in the gateway
// (once per token delta); taking *providers.StreamChunk instead of the any
// parameter of writeEvent avoids heap-boxing the chunk on every write.
func writeChunk(bw *bufio.Writer, enc *json.Encoder, chunk *providers.StreamChunk) error {
	if _, err := bw.WriteString("data: "); err != nil {
		return err
	}
	if err := enc.Encode(chunk); err != nil {
		return err
	}
	return bw.WriteByte('\n')
}

// writeEvent writes an arbitrary payload as an SSE event. It is used for
// low-frequency control events (errors, timeouts) where the any boxing is
// negligible; per-chunk writes use writeChunk instead.
func writeEvent(bw *bufio.Writer, enc *json.Encoder, payload any) error {
	if _, err := bw.WriteString("data: "); err != nil {
		return err
	}
	if err := enc.Encode(payload); err != nil {
		return err
	}
	return bw.WriteByte('\n')
}

func flush(controller *http.ResponseController) error {
	if err := controller.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}

func setWriteDeadline(controller *http.ResponseController, deadline time.Time) error {
	if err := controller.SetWriteDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}

func clearWriteDeadline(controller *http.ResponseController) error {
	return setWriteDeadline(controller, time.Time{})
}
