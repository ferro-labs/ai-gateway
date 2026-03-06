package core

// Message role constants used across multiple providers.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleSystem    = "system"
	RoleTool      = "tool"

	// ContentTypeText is the content-part type for plain text (multimodal messages).
	ContentTypeText = "text"

	// SSEDone is the sentinel value that marks the end of a server-sent event stream.
	SSEDone = "[DONE]"
)
