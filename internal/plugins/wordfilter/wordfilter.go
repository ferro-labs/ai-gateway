// Package wordfilter provides a word-filter guardrail plugin that rejects
// requests containing blocked words. Register it with a blank import:
//
//	_ "github.com/ferro-labs/ai-gateway/internal/plugins/wordfilter"
package wordfilter

import (
	"context"
	"strings"

	"github.com/ferro-labs/ai-gateway/plugin"
)

func init() {
	plugin.RegisterFactory("word-filter", func() plugin.Plugin {
		return &WordFilter{}
	})
}

// WordFilter is a guardrail plugin that blocks requests containing
// configurable blocked words or phrases.
type WordFilter struct {
	blockedWords  []string
	caseSensitive bool
}

// Name returns the plugin identifier.
func (w *WordFilter) Name() string { return "word-filter" }

// Type returns the plugin lifecycle hook type.
func (w *WordFilter) Type() plugin.PluginType { return plugin.TypeGuardrail }

// Init configures the plugin from the provided options map.
func (w *WordFilter) Init(config map[string]interface{}) error {
	if words, ok := config["blocked_words"]; ok {
		switch list := words.(type) {
		case []interface{}:
			for _, word := range list {
				if s, ok := word.(string); ok {
					w.blockedWords = append(w.blockedWords, s)
				}
			}
		case []string:
			w.blockedWords = append(w.blockedWords, list...)
		}
	}
	if cs, ok := config["case_sensitive"].(bool); ok {
		w.caseSensitive = cs
	}
	return nil
}

// Execute runs the plugin logic for the current request context.
func (w *WordFilter) Execute(_ context.Context, pctx *plugin.Context) error {
	if pctx.Request == nil || len(w.blockedWords) == 0 {
		return nil
	}

	for _, msg := range pctx.Request.Messages {
		content := msg.Content
		if !w.caseSensitive {
			content = strings.ToLower(content)
		}
		for _, word := range w.blockedWords {
			check := word
			if !w.caseSensitive {
				check = strings.ToLower(check)
			}
			if strings.Contains(content, check) {
				pctx.Reject = true
				pctx.Reason = "blocked word detected: " + word
				return nil
			}
		}
	}
	return nil
}
