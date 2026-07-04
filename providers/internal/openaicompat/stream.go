package openaicompat

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// DecodeStreamChunk decodes an OpenAI-compatible chat completion stream chunk
// into the gateway's canonical stream type. Providers should use this instead
// of local role/content-only structs so deltas like tool_calls, usage, and
// reasoning_content are preserved consistently.
func DecodeStreamChunk(data []byte) (core.StreamChunk, error) {
	var chunk core.StreamChunk
	if err := json.Unmarshal(data, &chunk); err != nil {
		return core.StreamChunk{}, fmt.Errorf("failed to unmarshal stream chunk: %w", err)
	}
	// Normalize provider-specific finish reasons to the canonical OpenAI
	// vocabulary for every OpenAI-compatible provider.
	for i := range chunk.Choices {
		chunk.Choices[i].FinishReason = core.NormalizeFinishReason(chunk.Choices[i].FinishReason)
	}
	return chunk, nil
}

// StreamSSE consumes an OpenAI-compatible SSE response body and returns a channel
// of decoded chunks. It takes ownership of body: a goroutine reads to completion
// (the terminating "[DONE]" sentinel, EOF, or a scan error), closes body, and
// closes the channel. Lines that fail to decode are skipped so benign non-JSON
// keep-alive frames don't abort an otherwise healthy stream; a scanner read
// error is surfaced as a final chunk.
//
// Callers must perform the non-200 status check before handing the body over.
func StreamSSE(body io.ReadCloser) <-chan core.StreamChunk {
	ch := make(chan core.StreamChunk)
	go func() {
		defer close(ch)
		defer func() { _ = body.Close() }()

		scanner := core.NewSSEScanner(body)
		for scanner.Scan() {
			data, ok := strings.CutPrefix(scanner.Text(), "data: ")
			if !ok {
				continue
			}
			if data == core.SSEDone {
				return
			}
			chunk, err := DecodeStreamChunk([]byte(data))
			if err != nil {
				continue
			}
			ch <- chunk
		}
		if err := scanner.Err(); err != nil {
			ch <- core.StreamChunk{Error: err}
		}
	}()
	return ch
}
