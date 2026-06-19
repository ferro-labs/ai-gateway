package core

import "encoding/json"

// ToolChoiceKind is the normalized form of the OpenAI `tool_choice` field,
// independent of any provider's native vocabulary.
type ToolChoiceKind int

const (
	// ToolChoiceUnset means the caller did not specify tool_choice.
	ToolChoiceUnset ToolChoiceKind = iota
	// ToolChoiceAuto lets the model decide whether to call a tool ("auto").
	ToolChoiceAuto
	// ToolChoiceNone forbids tool calls ("none").
	ToolChoiceNone
	// ToolChoiceRequired forces the model to call some tool ("required").
	ToolChoiceRequired
	// ToolChoiceFunction forces a specific named function.
	ToolChoiceFunction
)

// NormalizeToolChoice decodes the OpenAI `tool_choice` value (a string
// "auto"/"none"/"required", or an object {"type":"function","function":{"name":…}})
// into a provider-agnostic (kind, functionName). Unrecognized values normalize
// to ToolChoiceUnset so callers omit tool_choice rather than forwarding garbage.
// Each provider maps the result onto its own native vocabulary.
func NormalizeToolChoice(choice any) (kind ToolChoiceKind, functionName string) {
	switch v := choice.(type) {
	case nil:
		return ToolChoiceUnset, ""
	case string:
		switch v {
		case "auto":
			return ToolChoiceAuto, ""
		case "none":
			return ToolChoiceNone, ""
		case "required":
			return ToolChoiceRequired, ""
		default:
			return ToolChoiceUnset, ""
		}
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return ToolChoiceUnset, ""
		}
		var named struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		if json.Unmarshal(raw, &named) == nil && named.Type == "function" && named.Function.Name != "" {
			return ToolChoiceFunction, named.Function.Name
		}
		return ToolChoiceUnset, ""
	}
}
