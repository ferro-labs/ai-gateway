package core

// Message role constants used across multiple providers.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleSystem    = "system"
	RoleTool      = "tool"
	// RoleDeveloper is OpenAI's successor to the system role. Providers with a
	// dedicated system channel must treat it as a system turn — see IsSystemRole.
	RoleDeveloper = "developer"

	// ContentTypeText is the content-part type for plain text (multimodal messages).
	ContentTypeText = "text"

	// SSEDone is the sentinel value that marks the end of a server-sent event stream.
	SSEDone = "[DONE]"
)

// IsSystemRole reports whether a message role carries system instructions.
// Providers that route system turns through a dedicated field (Anthropic's
// system, Gemini's systemInstruction, Nova's system) use this so an OpenAI
// "developer" turn lands there instead of being forwarded as a role those APIs
// reject.
func IsSystemRole(role string) bool {
	return role == RoleSystem || role == RoleDeveloper
}
