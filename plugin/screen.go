package plugin

import (
	"fmt"
	"iter"
	"strings"

	"github.com/ferro-labs/ai-gateway/internal/envref"
	"github.com/ferro-labs/ai-gateway/providers"
)

// RequestText yields every piece of text a request carries to the provider:
// each message's Content and ReasoningContent, the Text of each of its content
// parts, and the arguments of each tool call it replays.
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
//
// A consumer that finds ctx.Err() set between texts should stop and return nil,
// not the context's error: an error from Execute means the plugin broke, which
// the gateway answers 500 and the target's circuit breaker counts as a fault.
// A caller hanging up is neither.
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

// yieldMessage yields every text-bearing field of a message — Content, the
// reasoning content, each part's Text and each tool call's arguments —
// skipping empties. A message built from content parts carries an empty
// Content, and a non-text part carries empty Text; yielding those would make
// every consumer screen the empty string once per absent field. An absent
// field is not content.
//
// Reasoning content and tool-call arguments are message text like the rest of
// it, and are the two fields a model fills when it is asked to act rather than
// to answer. A credential or a matched pattern placed in either used to leave
// the gateway unscreened while the guardrail reported itself enabled. A tool
// call's function NAME is not yielded: it names a capability the operator
// configured, not content either side supplied.
//
// The same fields travel INBOUND whenever a client replays a conversation, so
// they are screened in both directions: a string that is a violation on the way
// out is a violation on the way back in, and making the verdict depend on the
// direction would leave the replay unscreened.
func yieldMessage(msg providers.Message, yield func(string) bool) bool {
	if msg.Content != "" && !yield(msg.Content) {
		return false
	}
	if msg.ReasoningContent != "" && !yield(msg.ReasoningContent) {
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
	for _, call := range msg.ToolCalls {
		if call.Function.Arguments == "" {
			continue
		}
		if !yield(call.Function.Arguments) {
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
	if pctx == nil || pctx.Request == nil || pctx.Stage != StageBeforeRequest {
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
//
// The fallback must itself be in the allowed set. A caller passing one it cannot
// honour would turn an unset key into the same silent no-op this helper exists
// to prevent, one field over.
func NormalizeAction(raw, fallback string, allowed ...string) (string, error) {
	if !permitted(fallback, allowed) {
		return "", fmt.Errorf("fallback action %q is not one of %q", fallback, allowed)
	}
	action := strings.ToLower(strings.TrimSpace(raw))
	if action == "" {
		return fallback, nil
	}
	if !permitted(action, allowed) {
		return "", fmt.Errorf("unrecognized action %q: must be one of %q", raw, allowed)
	}
	return action, nil
}

// ValidateAction checks a configured action at CONFIG-LOAD time, against the
// same allowed set NormalizeAction will apply when the plugin is constructed.
// It is what a guardrail's ValidateConfig calls, so a misspelled action is a
// `ferrogw validate` error rather than a failed startup or a failed config
// reload.
//
// A value carrying a ${VAR} reference is passed rather than checked. Those
// resolve when the plugin is constructed, never at load, so at load the value
// is the reference itself and judging it would reject every deployment that
// names its action in the environment — making validate stricter than the
// server it is checking for. An unresolved reference is unvalidatable here, and
// the resolved value still meets NormalizeAction at Init.
func ValidateAction(raw any, fallback string, allowed ...string) error {
	action, err := StringSetting(raw, "action")
	if err != nil {
		return err
	}
	if envref.HasReference(action) {
		return nil
	}
	if _, err := NormalizeAction(action, fallback, allowed...); err != nil {
		return fmt.Errorf("action: %w", err)
	}
	return nil
}

// ValidateViaInit is a ConfigValidator body for a plugin whose Init is pure —
// no I/O, no environment — so the two share one parser as the contract asks:
// the block is handed to a fresh instance's Init and its verdict is the
// validator's. A block carrying a ${VAR} reference anywhere cannot be judged
// before the environment is available, so only its action is checked and the
// rest waits for Init at startup, where every value is resolved.
func ValidateViaInit(name string, config map[string]any, fallback string, allowed ...string) error {
	if envref.HasReferenceIn(config) {
		if err := ValidateAction(config["action"], fallback, allowed...); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		return nil
	}
	factory, ok := GetFactory(name)
	if !ok {
		return fmt.Errorf("%s: plugin is not registered", name)
	}
	return factory().Init(config)
}

func permitted(value string, allowed []string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, a := range allowed {
		if value == strings.ToLower(strings.TrimSpace(a)) {
			return true
		}
	}
	return false
}
