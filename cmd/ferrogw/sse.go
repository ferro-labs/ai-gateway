package main

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
	sseWriteDeadline = 15 * time.Second
	sseIdleTimeout   = 2 * time.Minute
)

// writeSSE streams SSE chunks from ch to the response writer.
func writeSSE(ctx context.Context, w http.ResponseWriter, ch <-chan providers.StreamChunk) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	controller := http.NewResponseController(w)
	_ = clearSSEWriteDeadline(controller)

	bw := bufio.NewWriterSize(w, 4096)
	enc := json.NewEncoder(bw)
	now := time.Now().Unix()
	idleTimer := time.NewTimer(sseIdleTimeout)
	defer idleTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			logging.FromContext(ctx).Debug("stream response canceled", "error", ctx.Err())
			return
		case <-idleTimer.C:
			logging.FromContext(ctx).Warn("stream response timed out waiting for next chunk", "idle_timeout_ms", sseIdleTimeout.Milliseconds())
			_ = writeAndFlushSSE(ctx, controller, bw, func() error {
				return writeSSEEvent(bw, enc, map[string]any{
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
				_ = writeAndFlushSSE(ctx, controller, bw, func() error {
					_, err := bw.WriteString("data: [DONE]\n\n")
					return err
				})
				return
			}

			if chunk.Error != nil {
				_ = writeAndFlushSSE(ctx, controller, bw, func() error {
					return writeSSEEvent(bw, enc, map[string]any{
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
			idleTimer.Reset(sseIdleTimeout)

			if err := writeAndFlushSSE(ctx, controller, bw, func() error {
				return writeSSEEvent(bw, enc, chunk)
			}); err != nil {
				if !errors.Is(err, context.Canceled) {
					logging.FromContext(ctx).Debug("stream response write failed", "error", err)
				}
				return
			}
		}
	}
}

func writeAndFlushSSE(ctx context.Context, controller *http.ResponseController, bw *bufio.Writer, writeFn func() error) error {
	if err := setSSEWriteDeadline(controller, time.Now().Add(sseWriteDeadline)); err != nil {
		return err
	}
	defer func() {
		_ = clearSSEWriteDeadline(controller)
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
	return flushSSE(controller)
}

func writeSSEEvent(bw *bufio.Writer, enc *json.Encoder, payload any) error {
	if _, err := bw.WriteString("data: "); err != nil {
		return err
	}
	if err := enc.Encode(payload); err != nil {
		return err
	}
	if err := bw.WriteByte('\n'); err != nil {
		return err
	}
	return nil
}

func flushSSE(controller *http.ResponseController) error {
	if err := controller.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}

func setSSEWriteDeadline(controller *http.ResponseController, deadline time.Time) error {
	if err := controller.SetWriteDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}

func clearSSEWriteDeadline(controller *http.ResponseController) error {
	return setSSEWriteDeadline(controller, time.Time{})
}
