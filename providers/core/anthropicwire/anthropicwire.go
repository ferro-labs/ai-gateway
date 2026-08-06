// Package anthropicwire holds request types shared by the standalone Anthropic
// provider and the Anthropic-on-Bedrock path, which target the same Messages API
// JSON shape over different transports.
package anthropicwire

import (
	"encoding/json"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// Tool is an Anthropic tool definition ({name, description, input_schema}).
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// MapTools converts canonical tool definitions to Anthropic's native shape.
// An empty parameter schema defaults to an empty object, which Anthropic
// requires (input_schema is mandatory).
func MapTools(tools []core.Tool) []Tool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]Tool, 0, len(tools))
	for _, t := range tools {
		schema := t.Function.Parameters
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, Tool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			InputSchema: schema,
		})
	}
	return out
}
