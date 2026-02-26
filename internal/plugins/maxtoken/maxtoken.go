// Package maxtoken provides a max-token guardrail plugin that caps the
// max_tokens and message count on outgoing requests. Register it with a blank import:
//
//	_ "github.com/ferro-labs/ai-gateway/internal/plugins/maxtoken"
package maxtoken

import (
	"context"
	"fmt"

	"github.com/ferro-labs/ai-gateway/plugin"
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

// Name returns the plugin identifier.
func (m *MaxToken) Name() string { return "max-token" }

// Type returns the plugin lifecycle hook type.
func (m *MaxToken) Type() plugin.PluginType { return plugin.TypeGuardrail }

// Init configures the plugin from the provided options map.
func (m *MaxToken) Init(config map[string]interface{}) error {
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

// Execute runs the plugin logic for the current request context.
func (m *MaxToken) Execute(_ context.Context, pctx *plugin.Context) error {
	if pctx.Request == nil {
		return nil
	}

	// Enforce max_tokens on request
	if pctx.Request.MaxTokens != nil && *pctx.Request.MaxTokens > m.maxTokens {
		pctx.Reject = true
		pctx.Reason = fmt.Sprintf("max_tokens %d exceeds limit of %d", *pctx.Request.MaxTokens, m.maxTokens)
		return nil
	}

	// Enforce max messages count
	if len(pctx.Request.Messages) > m.maxMessages {
		pctx.Reject = true
		pctx.Reason = fmt.Sprintf("message count %d exceeds limit of %d", len(pctx.Request.Messages), m.maxMessages)
		return nil
	}

	// Enforce max input length
	if m.maxInputLen > 0 {
		totalLen := 0
		for _, msg := range pctx.Request.Messages {
			totalLen += len(msg.Content)
		}
		if totalLen > m.maxInputLen {
			pctx.Reject = true
			pctx.Reason = fmt.Sprintf("total input length %d exceeds limit of %d", totalLen, m.maxInputLen)
			return nil
		}
	}

	return nil
}
