package openaicompat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// streamFrame is one OpenAI-compatible "data:" frame. An upstream that fails
// once the 200 headers are already out reports it in the same frame shape as a
// chunk — an error envelope in place of choices — so both are decoded in one
// pass. Decoding only the chunk part would turn the failure into a content-free
// delta and deliver the truncated answer as a success.
type streamFrame struct {
	core.StreamChunk
	Err *streamErrorEnvelope `json:"error"`
}

type streamErrorEnvelope struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// DecodeStreamChunk decodes an OpenAI-compatible chat completion stream chunk
// into the gateway's canonical stream type. Providers should use this instead
// of local role/content-only structs so deltas like tool_calls, usage, and
// reasoning_content are preserved consistently. A frame carrying a mid-stream
// error envelope is returned with Error set.
func DecodeStreamChunk(data []byte) (core.StreamChunk, error) {
	var frame streamFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		return core.StreamChunk{}, fmt.Errorf("failed to unmarshal stream chunk: %w", err)
	}
	chunk := frame.StreamChunk
	// Normalize provider-specific finish reasons to the canonical OpenAI
	// vocabulary for every OpenAI-compatible provider.
	for i := range chunk.Choices {
		chunk.Choices[i].FinishReason = core.NormalizeFinishReason(chunk.Choices[i].FinishReason)
	}
	// Only a populated message counts: providers that always emit the field send
	// "error": null on healthy frames, and a frame can carry content alongside it.
	if frame.Err != nil && frame.Err.Message != "" {
		chunk.Error = streamError(frame.Err.Type, frame.Err.Message)
	}
	return chunk, nil
}

// streamError formats a mid-stream error envelope. The type is optional — some
// OpenAI-compatible upstreams send only a message.
func streamError(typ, msg string) error {
	if typ == "" {
		return fmt.Errorf("stream error: %s", msg)
	}
	return fmt.Errorf("stream error (%s): %s", typ, msg)
}

// StreamSSE consumes an OpenAI-compatible SSE response body and returns a channel
// of decoded chunks. Frames are read with core.SSEDataLines, so the space after
// "data:" is optional as the SSE spec has it. It takes ownership of body: a
// goroutine reads to completion (the terminating "[DONE]" sentinel, EOF, or a
// scan error), closes body, and closes the channel. Lines that fail to decode
// are skipped so benign non-JSON keep-alive frames don't abort an otherwise
// healthy stream; a scanner read error is surfaced as a final chunk. A frame
// carrying an error envelope is forwarded with Error set and ends the stream —
// nothing valid follows it.
//
// Callers must perform the non-200 status check before handing the body over.
// ctx cancellation stops the reader promptly: a pending send is abandoned and
// body is closed, so a consumer that stops reading cannot leak the goroutine.
func StreamSSE(ctx context.Context, body io.ReadCloser) <-chan core.StreamChunk {
	ch := make(chan core.StreamChunk)
	go func() {
		defer close(ch)
		defer func() { _ = body.Close() }()

		lines, scanErr := core.SSEDataLines(body)
		for data := range lines {
			if data == core.SSEDone {
				return
			}
			chunk, err := DecodeStreamChunk([]byte(data))
			if err != nil {
				continue
			}
			if !core.SendChunk(ctx, ch, chunk) {
				return
			}
			if chunk.Error != nil {
				return
			}
		}
		if err := scanErr(); err != nil {
			core.SendChunk(ctx, ch, core.StreamChunk{Error: err})
		}
	}()
	return ch
}
