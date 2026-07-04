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

// MapToolChoice maps the OpenAI tool_choice value onto Anthropic's native
// tool_choice object. It returns nil when no tools are present (Anthropic rejects
// tool_choice without tools) and when the choice selects no concrete mode.
func MapToolChoice(choice any, tools []core.Tool) any {
	if len(tools) == 0 {
		return nil
	}
	switch kind, name := core.NormalizeToolChoice(choice); kind {
	case core.ToolChoiceAuto:
		return map[string]string{"type": "auto"}
	case core.ToolChoiceNone:
		return map[string]string{"type": "none"}
	case core.ToolChoiceRequired:
		return map[string]string{"type": "any"}
	case core.ToolChoiceFunction:
		return map[string]string{"type": "tool", "name": name}
	default:
		return nil
	}
}

// BuildMessages converts canonical chat messages into Anthropic Messages API
// messages plus the concatenated system prompt (system turns joined with "\n").
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
		switch msg.Role {
		case core.RoleSystem:
			systemParts = append(systemParts, msg.Content)
		case core.RoleTool:
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
			messages = append(messages, Message{
				Role:    msg.Role,
				Content: content(msg),
			})
		}
	}
	return messages, strings.Join(systemParts, "\n")
}
