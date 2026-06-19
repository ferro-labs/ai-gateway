package openaicompat

import (
	"encoding/json"
	"fmt"

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
	return chunk, nil
}
