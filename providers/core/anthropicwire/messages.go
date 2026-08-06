package anthropicwire

import (
	"encoding/json"
	"strings"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// Message is a single outbound message in an Anthropic Messages API request.
// Content is either a plain string (text-only turns) or a []Block (multimodal
// turns, assistant tool_use blocks, or user tool_result blocks).
type Message struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// Block is a single content block in an outbound message. Only the fields
// relevant to Type are populated; omitempty keeps each block minimal.
type Block struct {
	Type string `json:"type"`

	// type=text
	Text string `json:"text,omitempty"`

	// type=image
	Source *ImageSource `json:"source,omitempty"`

	// type=tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// type=tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
}

// ImageSource carries an image for an "image" content block. Anthropic accepts
// either inlined base64 data or a remote URL.
type ImageSource struct {
	Type      string `json:"type"`                 // "base64" | "url"
	MediaType string `json:"media_type,omitempty"` // base64 only
	Data      string `json:"data,omitempty"`       // base64 only
	URL       string `json:"url,omitempty"`        // url only
}

// MapToolChoice maps the OpenAI tool_choice and parallel_tool_calls values onto
// Anthropic's native tool_choice object. It returns nil when no tools are
// present (Anthropic rejects tool_choice without tools) and when neither field
// selects anything to say.
//
// Anthropic has no parallel_tool_calls field. Its equivalent is
// disable_parallel_tool_use, which is the inverse, defaults to false, and lives
// on the tool_choice object rather than at the top level — so it is expressible
// only through this mapping, and only on the auto, any and tool shapes (the
// none shape does not carry it, and with no tool use to parallelise the
// question does not arise).
//
// Two consequences follow. A caller that sends parallel_tool_calls:false with
// tools but no tool_choice gets a synthesized {"type":"auto"} to hang the flag
// on — "auto" is the default Anthropic would have applied anyway, so this adds
// the caller's constraint without changing what was requested. And the flag is
// emitted only for an explicit false: parallel_tool_calls:true and an absent
// field both leave the upstream default in place rather than writing it out.
func MapToolChoice(req core.Request) any {
	if len(req.Tools) == 0 {
		return nil
	}
	disableParallel := req.ParallelToolCalls != nil && !*req.ParallelToolCalls

	choice := map[string]any{}
	switch kind, name := core.NormalizeToolChoice(req.ToolChoice); kind {
	case core.ToolChoiceAuto:
		choice["type"] = "auto"
	case core.ToolChoiceNone:
		// "none" means no tool will be called, so there is nothing to
		// serialize; Anthropic's schema omits the flag on this shape.
		return map[string]any{"type": "none"}
	case core.ToolChoiceRequired:
		choice["type"] = "any"
	case core.ToolChoiceFunction:
		choice["type"] = "tool"
		choice["name"] = name
	default:
		if !disableParallel {
			return nil
		}
		choice["type"] = "auto"
	}
	if disableParallel {
		choice["disable_parallel_tool_use"] = true
	}
	return choice
}

// BuildMessages converts canonical chat messages into Anthropic Messages API
// messages plus the concatenated system prompt (system and developer turns
// joined with "\n").
// Tool-result turns become tool_result blocks merged into the preceding user
// turn so parallel results share one message; every other turn is rendered by
// content, which returns either a plain string or a []Block. content lets each
// transport keep its own block-building rules (image handling, argument
// validation); it MUST return []Block (not another slice type) when it emits
// blocks so tool_result merging can locate the previous turn's blocks.
func BuildMessages(req core.Request, content func(core.Message) any) ([]Message, string) {
	var systemParts []string
	var messages []Message

	for _, msg := range req.Messages {
		switch {
		case core.IsSystemRole(msg.Role):
			systemParts = append(systemParts, msg.Content)
		case msg.Role == core.RoleTool:
			block := Block{
				Type:      "tool_result",
				ToolUseID: msg.ToolCallID,
				Content:   msg.Content,
			}
			if n := len(messages); n > 0 && messages[n-1].Role == core.RoleUser {
				if blocks, ok := messages[n-1].Content.([]Block); ok {
					blocks = append(blocks, block)
					messages[n-1].Content = blocks
					continue
				}
			}
			messages = append(messages, Message{
				Role:    core.RoleUser,
				Content: []Block{block},
			})
		default:
			c := content(msg)
			// A turn whose parts are all kinds this transport cannot render
			// yields no blocks; send the collapsed text instead of a null
			// content the API rejects.
			if blocks, ok := c.([]Block); ok && len(blocks) == 0 {
				c = msg.Content
			}
			messages = append(messages, Message{
				Role:    msg.Role,
				Content: c,
			})
		}
	}
	return messages, strings.Join(systemParts, "\n")
}
