package plugin

import (
	"fmt"
	"iter"
	"strings"

	"github.com/ferro-labs/ai-gateway/providers"
)

// RequestText yields every piece of text a request carries to the provider:
// each message's Content, then the Text of each of its content parts.
//
// Both, because neither alone is the whole message. Message.UnmarshalJSON
// collapses only parts typed "text" into Content, so a blocked word in a part
// of any other type leaves no trace in Content and is forwarded upstream;
// a message built in Go rather than decoded from JSON has parts and an empty
// Content. Re-scanning the collapsed text costs a second pass over bytes
// already in cache and cannot produce a wrong answer; missing a part can.
//
// Part.ImageURL is deliberately not yielded. A content policy is a policy about
// prose, and a data URI is base64 in which any short word appears by chance.
// Screening image text is OCR, not string matching.
func RequestText(req *providers.Request) iter.Seq[string] {
	return func(yield func(string) bool) {
		if req == nil {
			return
		}
		for _, msg := range req.Messages {
			if !yieldMessage(msg, yield) {
				return
			}
		}
	}
}

// ResponseText yields every piece of text a response carries back to the
// caller, on the same terms as RequestText.
func ResponseText(resp *providers.Response) iter.Seq[string] {
	return func(yield func(string) bool) {
		if resp == nil {
			return
		}
		for _, choice := range resp.Choices {
			if !yieldMessage(choice.Message, yield) {
				return
			}
		}
	}
}

// yieldMessage yields a message's Content and then each part's Text, skipping
// empties. A message built from content parts carries an empty Content, and a
// non-text part carries empty Text; yielding those would make every consumer
// screen the empty string once per absent field. An absent field is not
// content.
func yieldMessage(msg providers.Message, yield func(string) bool) bool {
	if msg.Content != "" && !yield(msg.Content) {
		return false
	}
	for _, part := range msg.ContentParts {
		if part.Text == "" {
			continue
		}
		if !yield(part.Text) {
			return false
		}
	}
	return true
}

// RejectUninspectable denies a before_request whose content the gateway could
// not project as text — an embeddings input sent as token IDs, which is a
// lossless encoding of the exact text a content policy screens. It reports
// whether it denied.
//
// Passing such a request would make every content policy evadable by one
// tokenizer call on the client. A verdict, not an error: the plugin reached a
// decision, so the caller gets a 4xx and not a 500. The reason says the content
// could not be read and no more, which leaks nothing about the policy while
// still telling the caller the one thing that fixes the request — send text.
//
// At after_request there is nothing to withhold: the response has already been
// delivered chunk by chunk, so this reports false and the caller proceeds.
func RejectUninspectable(pctx *Context) bool {
	if pctx == nil || pctx.Stage != StageBeforeRequest {
		return false
	}
	uninspectable, _ := pctx.Metadata[MetadataUninspectableContent].(bool)
	if !uninspectable {
		return false
	}
	pctx.Reject = true
	pctx.Reason = "request blocked by content policy: content is not inspectable text"
	return true
}

// Action names what a guardrail does when its check matches. Shared spellings
// so plugins with the same concept do not each invent their own strings.
const (
	ActionBlock  = "block"
	ActionWarn   = "warn"
	ActionLog    = "log"
	ActionRedact = "redact"
)

// NormalizeAction canonicalises a configured action against the set of actions
// the calling plugin can actually honour, and rejects anything outside it.
//
// The allowed set is the caller's because it differs per plugin: a filter
// blocks, warns or logs, while a redactor blocks or redacts and has no
// non-blocking observe mode. Accepting an action a plugin then ignores is the
// same failure as accepting a misspelled one — a guardrail the operator
// configured, the catalog reports as enabled, and which does not do what it
// says.
//
// A misspelling must fail the load rather than degrade to the nearest
// non-blocking behaviour. An empty value is not a misspelling: it means the key
// was not set, so it takes fallback.
func NormalizeAction(raw, fallback string, allowed ...string) (string, error) {
	action := strings.ToLower(strings.TrimSpace(raw))
	if action == "" {
		return fallback, nil
	}
	for _, a := range allowed {
		if action == strings.ToLower(strings.TrimSpace(a)) {
			return action, nil
		}
	}
	return "", fmt.Errorf("unrecognized action %q: must be one of %q", raw, allowed)
}
