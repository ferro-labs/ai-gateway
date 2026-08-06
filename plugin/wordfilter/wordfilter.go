// Package wordfilter provides a word-filter guardrail plugin that rejects
// requests containing blocked words. Register it with a blank import:
//
//	_ "github.com/ferro-labs/ai-gateway/plugin/wordfilter"
package wordfilter

import (
	"context"
	"strings"

	"github.com/ferro-labs/ai-gateway/pkg/logger"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

func init() {
	plugin.RegisterFactory("word-filter", func() plugin.Plugin {
		return &WordFilter{}
	})
}

// WordFilter is a guardrail plugin that blocks requests containing
// configurable blocked words or phrases.
//
// A blocked entry matches as a SUBSTRING, anywhere in the text and without
// regard to word boundaries: "class" is blocked by "ass". That is the safe
// direction for a blocklist — a boundary-aware matcher is evaded by
// punctuation, concatenation and every zero-width character, and each miss is a
// prompt that reached the provider. An operator who needs whole-word matching
// is asking for a different mode, not for this one to be loosened.
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

// Execute screens whichever side of the exchange the current stage carries: the
// request's messages at before_request, the response's choices at after_request.
//
// Listing the plugin at after_request used to re-screen the *request*, so a blocked
// phrase in the model's answer was served with a 200 while a blocked phrase in the
// prompt was rejected twice — once correctly as a 400, then again as a baffling 502
// on the response. An operator who lists a content guardrail after the request means
// to screen what came back.
//
// On a streaming response the after_request stage runs once the chunks have already
// been delivered, so it can report the violation but cannot withhold it. Screening
// prompts is what before_request is for.
func (w *WordFilter) Execute(ctx context.Context, pctx *plugin.Context) error {
	if len(w.blockedWords) == 0 {
		return nil
	}

	if pctx.Stage == plugin.StageAfterRequest {
		if pctx.Response == nil {
			return nil
		}
		for _, choice := range pctx.Response.Choices {
			if w.screen(ctx, pctx, choice.Message, "response") {
				return nil
			}
		}
		return nil
	}

	if pctx.Request == nil {
		return nil
	}
	if uninspectable, _ := pctx.Metadata[plugin.MetadataUninspectableContent].(bool); uninspectable &&
		pctx.Stage == plugin.StageBeforeRequest {
		// Content this filter was required to screen and could not read: an
		// embeddings input sent as token IDs, which is the same text a blocked
		// word is written in, merely encoded. Passing it would make the whole
		// blocklist evadable by one tokenizer call on the client — every
		// comparable policy engine denies here rather than fails open.
		//
		// A verdict, not an error: this plugin reached a decision, so the caller
		// gets a 4xx and not a 500. It says the content could not be read and no
		// more, which leaks nothing about the blocklist while still telling the
		// caller the one thing that fixes the request — send text.
		pctx.Reject = true
		pctx.Reason = "request blocked by content policy: content is not inspectable text"
		return nil
	}
	for _, msg := range pctx.Request.Messages {
		if w.screen(ctx, pctx, msg, "request") {
			return nil
		}
	}
	return nil
}

// screen checks every piece of text one message carries to the provider:
// Content, and the text of each content part.
//
// Both, because neither alone is the whole message. Message.UnmarshalJSON
// collapses only parts typed "text" into Content, so a blocked word in a part
// of any other type left no trace in Content and was forwarded upstream with a
// 200 — while a message built in Go rather than decoded from JSON has parts and
// an empty Content. Re-scanning the collapsed text costs a second pass over
// bytes already in cache and cannot produce a wrong answer; missing a part can.
//
// Part.ImageURL is deliberately not screened: a blocklist is a policy about
// prose, and a data URI is base64 in which any three-letter word appears by
// chance. Screening image text is OCR, not string matching.
func (w *WordFilter) screen(ctx context.Context, pctx *plugin.Context, msg providers.Message, subject string) bool {
	if w.reject(ctx, pctx, msg.Content, subject) {
		return true
	}
	for _, part := range msg.ContentParts {
		if w.reject(ctx, pctx, part.Text, subject) {
			return true
		}
	}
	return false
}

// reject screens one piece of content and, on a match, records the verdict.
// It reports whether the content was blocked.
//
// The matched word is logged server-side only and never reaches the client-facing
// reason, which would leak the operator's blocklist one probe at a time.
func (w *WordFilter) reject(ctx context.Context, pctx *plugin.Context, content, subject string) bool {
	if !w.caseSensitive {
		content = strings.ToLower(content)
	}
	for i, word := range w.blockedWords {
		check := word
		if !w.caseSensitive {
			check = w.loweredWords[i]
		}
		if strings.Contains(content, check) {
			logger.Ctx(ctx).Info("word-filter: blocked "+subject, "matched_word", word)
			pctx.Reject = true
			pctx.Reason = subject + " blocked by content policy"
			return true
		}
	}
	return false
}

// Close releases plugin resources.
func (w *WordFilter) Close() error { return nil }
