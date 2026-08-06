// Package sse provides Server-Sent Events streaming for OpenAI-compatible responses.
package sse

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/redact"
	"github.com/ferro-labs/ai-gateway/internal/streamio"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
	"github.com/ferro-labs/ai-gateway/providers"
)

// idleTimeout bounds the gap between two chunks arriving on the provider
// channel. The proxy and legacy-completions paths bound their upstream reads
// with streamio.IdleTimeout instead.
var idleTimeout = 2 * time.Minute

// SetIdleTimeoutForTest overrides the idle timeout for testing and returns a restore function.
func SetIdleTimeoutForTest(d time.Duration) func() {
	prev := idleTimeout
	idleTimeout = d
	return func() { idleTimeout = prev }
}

// Write streams SSE chunks from ch to the response writer.
//
// cancelStream cancels the context the producer runs under. Write uses it to
// report WHY it stopped when its own idle bound fires: the producer is torn down
// either way — the handler returning cancels the request context — but an
// anonymous cancellation is indistinguishable from the caller hanging up, and a
// stalled upstream was recorded as the caller's doing because of it. A nil
// cancelStream keeps that older behaviour for a caller that owns no producer
// context.
func Write(ctx context.Context, w http.ResponseWriter, ch <-chan providers.StreamChunk, cancelStream context.CancelCauseFunc) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	controller := http.NewResponseController(w)
	_ = streamio.ClearWriteDeadline(controller)

	bw := bufio.NewWriterSize(w, 4096)
	enc := json.NewEncoder(bw)
	now := time.Now().Unix()
	// One normalizer per stream: it holds the id and model seen so far and
	// which choices have already been given a role delta. See its godoc for
	// which parts of the OpenAI streaming contract it enforces.
	var envelope providers.StreamNormalizer
	idleTimer := time.NewTimer(idleTimeout)
	defer idleTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Ctx(ctx).Debug("stream response canceled", "error", ctx.Err())
			return
		case <-idleTimer.C:
			logger.Ctx(ctx).Warn("stream response timed out waiting for next chunk", "idle_timeout_ms", idleTimeout.Milliseconds())
			_ = writeAndFlush(ctx, controller, bw, func() error {
				return writeEvent(bw, enc, map[string]any{
					"error": map[string]string{
						"message": "stream timed out waiting for next chunk",
						"type":    "timeout_error",
						"code":    "stream_timeout",
					},
				})
			})
			// Strictly after the write: writeAndFlush refuses to write on a
			// cancelled context, so naming the cause first would silence the one
			// surface that already reported this correctly — the client's.
			if cancelStream != nil {
				cancelStream(streamio.ErrIdleTimeout)
			}
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
							// Mid-stream failures carry provider-controlled text,
							// which can quote the gateway's own credential.
							"message": redact.ErrorMessage(chunk.Error),
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
			envelope.Normalize(&chunk)
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
					logger.Ctx(ctx).Debug("stream response write failed", "error", err)
				}
				return
			}
		}
	}
}

func writeAndFlush(ctx context.Context, controller *http.ResponseController, bw *bufio.Writer, writeFn func() error) error {
	return streamio.WriteAndFlush(ctx, controller, bw.Flush, writeFn)
}

// writeChunk writes a single stream chunk as an SSE event using a
// concrete-typed argument. This is the highest-frequency write in the gateway
// (once per token delta); taking *providers.StreamChunk instead of the any
// parameter of writeEvent avoids heap-boxing the chunk on every write.
func writeChunk(bw *bufio.Writer, enc *json.Encoder, chunk *providers.StreamChunk) error {
	if chunk.Choices == nil {
		// choices is an array in the OpenAI schema, never null: a usage-only
		// terminal frame still carries an empty one, and clients index it
		// unconditionally. The chunk is borrowed, so normalise a copy.
		normalized := *chunk
		normalized.Choices = []providers.StreamChoice{}
		chunk = &normalized
	}
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
