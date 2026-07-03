package anthropicwire

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// BlockTypeToolUse is the Anthropic content-block type for a tool call, shared
// by the request builders and the response/stream decoders.
const BlockTypeToolUse = "tool_use"

// Usage is the token accounting Anthropic returns on a Messages response and on
// streaming message_start / message_delta events. The Anthropic-on-Bedrock path
// does not report the cache fields, which stay zero there.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// ContentBlock is one block of an Anthropic Messages response (a text block or a
// tool_use block). Only the fields relevant to Type are populated.
type ContentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// Response is the non-streaming Anthropic Messages API response body, shared by
// the standalone Anthropic provider and the Anthropic-on-Bedrock path (which
// target the same JSON shape over different transports).
type Response struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Role       string         `json:"role"`
	Content    []ContentBlock `json:"content"`
	Model      string         `json:"model"`
	StopReason string         `json:"stop_reason"`
	Usage      Usage          `json:"usage"`
}

// DecodeContent renders response content blocks into concatenated assistant text
// plus canonical tool calls. Text blocks are joined in order; each tool_use
// block becomes a core.ToolCall whose Arguments default to "{}" when the block
// carries no input.
func DecodeContent(blocks []ContentBlock) (text string, toolCalls []core.ToolCall) {
	var b strings.Builder
	for _, block := range blocks {
		switch block.Type {
		case "text":
			b.WriteString(block.Text)
		case BlockTypeToolUse:
			args := string(block.Input)
			if args == "" {
				args = "{}"
			}
			toolCalls = append(toolCalls, core.ToolCall{
				ID:   block.ID,
				Type: "function",
				Function: core.FunctionCall{
					Name:      block.Name,
					Arguments: args,
				},
			})
		}
	}
	return b.String(), toolCalls
}

// ParseDataURI splits a data URI of the form
// "data:<media-type>[;param]...;base64,<data>" into its media type and base64
// payload. ok is false for any non-base64 data URI or a plain remote URL,
// letting each transport decide how to handle those (the native Anthropic API
// accepts remote URLs; Bedrock does not). The "base64" token may appear after
// other parameters (e.g. "data:image/png;charset=utf-8;base64,...").
func ParseDataURI(uri string) (mediaType, data string, ok bool) {
	const prefix = "data:"
	if !strings.HasPrefix(uri, prefix) {
		return "", "", false
	}
	meta, payload, found := strings.Cut(uri[len(prefix):], ",")
	if !found {
		return "", "", false
	}
	params := strings.Split(meta, ";")
	if slices.Contains(params[1:], "base64") {
		return params[0], payload, true
	}
	return "", "", false
}
