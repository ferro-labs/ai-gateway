// Package maxtoken provides a max-token guardrail that REJECTS a request
// exceeding a configured limit. Register it with a blank import:
//
//	_ "github.com/ferro-labs/ai-gateway/plugin/maxtoken"
//
// # Scope
//
// It is reject-only. It never writes to the request, so max_tokens bounds the
// ceiling a CALLER DECLARED and nothing else: a request that sets neither
// max_tokens nor max_completion_tokens declares no ceiling, passes, and runs to
// the provider's own default. That is deliberate. Injecting a ceiling the
// caller never asked for is transform behaviour in a guardrail — it changes
// what is sent upstream, and the change shows up in the provider's bill and in
// truncated completions nobody configured. An operator who needs a hard
// completion bound sets it on the client or picks a model whose default is the
// bound they want.
//
// max_messages and max_input_length have no such gap: both measure something
// every request carries.
package maxtoken

import (
	"context"
	"fmt"

	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

func init() {
	plugin.RegisterFactory("max-token", func() plugin.Plugin {
		return &MaxToken{}
	})
}

// MaxToken is a guardrail plugin that enforces a maximum token limit
// on requests. It checks the max_tokens field and message length.
type MaxToken struct {
	maxTokens   int
	maxMessages int
	maxInputLen int
}

var _ plugin.ContentAgnostic = (*MaxToken)(nil)

// Name returns the plugin identifier.
func (m *MaxToken) Name() string { return "max-token" }

// Type returns the plugin lifecycle hook type.
func (m *MaxToken) Type() plugin.PluginType { return plugin.TypeGuardrail }

// Init configures the plugin from the provided options map.
//
// Every limit uses the same convention: 0 disables it. An operator who wants a
// guardrail off for one dimension writes 0 for that key and gets the other two
// unchanged. Omitting a key keeps its default (max_tokens 4096, max_messages
// 100); max_input_length has no default and is off until configured.
func (m *MaxToken) Init(config map[string]any) error {
	m.maxTokens = 4096 // default
	if v, ok := config["max_tokens"]; ok {
		switch val := v.(type) {
		case float64:
			m.maxTokens = int(val)
		case int:
			m.maxTokens = val
		}
	}
	m.maxMessages = 100 // default
	if v, ok := config["max_messages"]; ok {
		switch val := v.(type) {
		case float64:
			m.maxMessages = int(val)
		case int:
			m.maxMessages = val
		}
	}
	m.maxInputLen = 0 // 0 = no limit
	if v, ok := config["max_input_length"]; ok {
		switch val := v.(type) {
		case float64:
			m.maxInputLen = int(val)
		case int:
			m.maxInputLen = val
		}
	}
	return nil
}

// IgnoresRequestContent reports that this guardrail can reach its verdict
// without reading what the request actually says — true unless
// max_input_length is configured.
//
// The other two limits are counts, not content. max_tokens is the caller's own
// declared ceiling, and max_messages is the length of the message list; neither
// changes when the text inside those messages cannot be read. max_input_length
// is the exception: it MEASURES the content, so a body that projects to nothing
// satisfies it at length zero, which is exactly the vacuous approval the
// pass-through's refusal exists to prevent.
//
// Answering here rather than at Type() keeps the enforcement role honest: this
// is a guardrail either way, and it fails closed either way. See
// plugin.ContentAgnostic.
func (m *MaxToken) IgnoresRequestContent() bool { return m.maxInputLen == 0 }

// Execute runs the plugin logic for the current request context.
func (m *MaxToken) Execute(_ context.Context, pctx *plugin.Context) error {
	if pctx.Request == nil {
		return nil
	}

	// Enforce the request's completion-length ceiling, however it is expressed.
	//
	// EffectiveMaxTokens, not Request.MaxTokens: max_completion_tokens
	// supersedes max_tokens, and providers on the OpenAI API surface forward
	// only the former. Reading max_tokens alone let a request pair a small
	// max_tokens with a huge max_completion_tokens and have the huge one
	// forwarded past this cap.
	if requested, ok := pctx.Request.EffectiveMaxTokens(); m.maxTokens > 0 && ok && requested > m.maxTokens {
		pctx.Reject = true
		pctx.Reason = fmt.Sprintf("max_tokens %d exceeds limit of %d", requested, m.maxTokens)
		return nil
	}

	// Enforce max messages count.
	//
	// Only on a chat-shaped request. Embeddings and image generation reach the
	// plugin stages through a projection that turns each input element into one
	// user message, so a 150-document batch embedding arrives here as 150
	// messages — and a conversation-turn ceiling is not a statement about how
	// many documents may be embedded at once. The size limits below still
	// apply, because total input length means the same thing either way.
	_, projected := pctx.Metadata[plugin.MetadataSurface]
	if !projected && m.maxMessages > 0 && len(pctx.Request.Messages) > m.maxMessages {
		pctx.Reject = true
		pctx.Reason = fmt.Sprintf("message count %d exceeds limit of %d", len(pctx.Request.Messages), m.maxMessages)
		return nil
	}

	// Enforce max input length
	if m.maxInputLen > 0 {
		totalLen := 0
		for _, msg := range pctx.Request.Messages {
			totalLen += messageLen(msg)
		}
		if totalLen > m.maxInputLen {
			pctx.Reject = true
			pctx.Reason = fmt.Sprintf("total input length %d exceeds limit of %d", totalLen, m.maxInputLen)
			return nil
		}
	}

	return nil
}

// messageLen measures a message the size it travels to the provider. A
// multipart message is measured from its parts alone: Content already holds
// the concatenated text parts, so adding both would count the text twice while
// still ignoring the image payload — which, for a base64 data URI, is nearly
// the whole request.
func messageLen(msg providers.Message) int {
	if len(msg.ContentParts) == 0 {
		return len(msg.Content)
	}
	n := 0
	for _, part := range msg.ContentParts {
		n += len(part.Text)
		if part.ImageURL != nil {
			n += len(part.ImageURL.URL)
		}
	}
	return n
}

// Close releases plugin resources.
func (m *MaxToken) Close() error { return nil }
