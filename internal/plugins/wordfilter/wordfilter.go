// Package wordfilter provides a word-filter guardrail plugin that rejects
// requests containing blocked words. Register it with a blank import:
//
//	_ "github.com/ferro-labs/ai-gateway/internal/plugins/wordfilter"
package wordfilter

import (
	"context"
	"strings"

	"github.com/ferro-labs/ai-gateway/internal/logging"
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
	loweredWords  []string
	caseSensitive bool
}

// Name returns the plugin identifier.
func (w *WordFilter) Name() string { return "word-filter" }

// Type returns the plugin lifecycle hook type.
func (w *WordFilter) Type() plugin.PluginType { return plugin.TypeGuardrail }

// Init configures the plugin from the provided options map.
func (w *WordFilter) Init(config map[string]any) error {
	if words, ok := config["blocked_words"]; ok {
		switch list := words.(type) {
		case []any:
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
	// Pre-lowercase the blocked words once so case-insensitive Execute calls
	// compare against the cached list instead of calling strings.ToLower per
	// blocked-word × message × request.
	if !w.caseSensitive {
		w.loweredWords = make([]string, len(w.blockedWords))
		for i, word := range w.blockedWords {
			w.loweredWords[i] = strings.ToLower(word)
		}
	}
	return nil
}

// Execute runs the plugin logic for the current request context.
func (w *WordFilter) Execute(ctx context.Context, pctx *plugin.Context) error {
	if pctx.Request == nil || len(w.blockedWords) == 0 {
		return nil
	}

	for _, msg := range pctx.Request.Messages {
		content := msg.Content
		if !w.caseSensitive {
			content = strings.ToLower(content)
		}
		for i, word := range w.blockedWords {
			var check string
			if w.caseSensitive {
				check = word
			} else {
				check = w.loweredWords[i]
			}
			if strings.Contains(content, check) {
				// Log the matched word server-side only; never surface it in
				// the client-facing rejection reason to avoid leaking the
				// operator's blocklist.
				logging.FromContext(ctx).Info("word-filter: blocked request",
					"matched_word", word)
				pctx.Reject = true
				pctx.Reason = "request blocked by content policy"
				return nil
			}
		}
	}
	return nil
}

// Close releases plugin resources.
func (w *WordFilter) Close() error { return nil }
