// Package piiredact provides a pii-redact guardrail plugin that detects
// personally identifiable information and either denies the request or rewrites
// it with the values removed. Register it with a blank import:
//
//	_ "github.com/ferro-labs/ai-gateway/plugin/piiredact"
package piiredact

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/ferro-labs/ai-gateway/pkg/logger"
	"github.com/ferro-labs/ai-gateway/plugin"
)

func init() {
	plugin.RegisterFactory("pii-redact", func() plugin.Plugin {
		return &PIIRedact{}
	})
}

const defaultPlaceholder = "[REDACTED]"

type entity struct {
	name string
	re   *regexp.Regexp
	// verify, when set, decides whether a regex match is a real instance of
	// the entity. nil means every match counts.
	verify func(match string) bool
}

// matches reports whether text carries at least one verified instance.
func (e entity) matches(text string) bool {
	if e.verify == nil {
		return e.re.MatchString(text)
	}
	return slices.ContainsFunc(e.re.FindAllString(text, -1), e.verify)
}

// builtinEntities are the entity types recognised without configuration.
//
// The set is deliberately small and literal: each entry matches a well-known
// written form rather than guessing from context, because a false positive on
// this plugin costs a caller their request.
var builtinEntities = []entity{
	{name: "email", re: regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)},
	{name: "phone", re: regexp.MustCompile(`(\+1\d{10}|\(\d{3}\)\s?\d{3}-\d{4}|\d{3}[-.]\d{3}[-.]\d{4})`)},
	{name: "ssn", re: regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`)},
	// Sixteen digits alone also describe an order id or a tracking number, so
	// a match only counts when its check digit passes Luhn — the test every
	// issued card number satisfies and a random sixteen-digit string fails
	// nine times in ten.
	{name: "credit_card", re: regexp.MustCompile(`\b\d{4}[\s\-]?\d{4}[\s\-]?\d{4}[\s\-]?\d{4}\b`), verify: luhnValid},
}

// luhnValid reports whether the digits in s satisfy the Luhn check.
func luhnValid(s string) bool {
	sum, double := 0, false
	for i := len(s) - 1; i >= 0; i-- {
		c := s[i]
		if c < '0' || c > '9' {
			continue
		}
		d := int(c - '0')
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum%10 == 0
}

// PIIRedact detects PII and either denies the request or sanitizes it.
//
// With action "redact" the plugin REWRITES Request.Messages in place and lets
// the request continue. That is the point of the mode: the provider never sees
// the value, and the caller still gets an answer. With action "block" nothing is
// rewritten and the request is denied.
//
// Redaction takes effect on the chat-shaped surfaces — /v1/chat/completions,
// streaming chat and /v1/completions — where the gateway reads the rewritten
// request back before routing it. Every other surface projects its own body
// into Request for screening and forwards that body unchanged, so a rewrite
// there would be discarded. On those surfaces a detection is DENIED instead
// (plugin.MetadataSurface is how they are recognised): reporting a redaction
// that did not happen would forward the exact value the plugin claims to
// remove.
//
// With action "log" nothing is denied or rewritten: a detection is recorded
// by entity type and the request is forwarded as written. That is the
// observe-only mode for sizing a policy before enforcing it, and it behaves
// the same on every surface, since it has no rewrite to lose.
type PIIRedact struct {
	entities    []entity
	action      string
	placeholder string
	// jsonPlaceholder is placeholder as it reads inside a JSON string, for the
	// one field that is a JSON document: a tool call's arguments.
	jsonPlaceholder string
}

// Name returns the plugin identifier.
func (p *PIIRedact) Name() string { return "pii-redact" }

// Type returns the plugin lifecycle hook type.
func (p *PIIRedact) Type() plugin.PluginType { return plugin.TypeGuardrail }

// SupportedStages reports that this plugin screens the request only. Its
// redact mode rewrites the request the gateway is about to route, which has no
// meaning once the provider has answered, so an entry at another stage would
// enforce nothing and is refused at load instead.
func (p *PIIRedact) SupportedStages() []plugin.Stage {
	return []plugin.Stage{plugin.StageBeforeRequest}
}

// actions is the closed set this plugin honours; see plugin.NormalizeAction.
var actions = []string{plugin.ActionBlock, plugin.ActionRedact, plugin.ActionLog}

// ValidateConfig runs the same checks Init runs, so a misconfiguration is a
// `ferrogw validate` error rather than a failed start; see plugin.ValidateViaInit.
func (p *PIIRedact) ValidateConfig(config map[string]any) error {
	return plugin.ValidateViaInit("pii-redact", config, plugin.ActionBlock, actions...)
}

// Init selects the entity set and the action.
func (p *PIIRedact) Init(config map[string]any) error {
	rawAction, err := plugin.StringSetting(config["action"], "action")
	if err != nil {
		return fmt.Errorf("pii-redact: %w", err)
	}
	action, err := plugin.NormalizeAction(rawAction, plugin.ActionBlock, actions...)
	if err != nil {
		return fmt.Errorf("pii-redact: action: %w", err)
	}
	p.action = action

	placeholder, err := plugin.StringSetting(config["redact_placeholder"], "redact_placeholder")
	if err != nil {
		return fmt.Errorf("pii-redact: %w", err)
	}
	p.placeholder = defaultPlaceholder
	if strings.TrimSpace(placeholder) != "" {
		p.placeholder = placeholder
	}
	escaped, err := json.Marshal(p.placeholder)
	if err != nil {
		return fmt.Errorf("pii-redact: redact_placeholder: %w", err)
	}
	p.jsonPlaceholder = string(escaped[1 : len(escaped)-1])

	entities, present := config["entities"]
	selected, err := selectEntities(entities, present)
	if err != nil {
		return err
	}
	p.entities = selected

	custom, err := plugin.ListSetting(config["patterns"], "patterns")
	if err != nil {
		return fmt.Errorf("pii-redact: %w", err)
	}
	for i, v := range custom {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("pii-redact: patterns[%d] must be a string", i)
		}
		// An empty pattern compiles and matches every string, so it would deny
		// every request under "block" and rewrite every request under "redact".
		// An empty entry in a list is a typo, never a policy.
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("pii-redact: patterns[%d] requires a non-empty pattern", i)
		}
		re, err := regexp.Compile(s)
		if err != nil {
			return fmt.Errorf("pii-redact: patterns[%d]: %w", i, err)
		}
		p.entities = append(p.entities, entity{name: fmt.Sprintf("custom_%d", i+1), re: re})
	}

	// Checked after the custom patterns are appended, because an empty entities
	// list alongside patterns is a real policy — screen mine and none of the
	// built-ins. Only a plugin left with no detector at all is the defect:
	// enabled in the catalog, detecting nothing. Unreachable with the key
	// absent, which selects every built-in entity.
	if len(p.entities) == 0 {
		return fmt.Errorf("pii-redact: entities is empty: omit the key to select every entity, or name at least one")
	}
	return nil
}

// Execute screens the request. An absent "entities" list means every builtin.
func (p *PIIRedact) Execute(ctx context.Context, pctx *plugin.Context) error {
	if len(p.entities) == 0 || pctx.Stage != plugin.StageBeforeRequest {
		return nil
	}
	if plugin.RejectUninspectable(pctx) {
		return nil
	}
	if pctx.Request == nil {
		return nil
	}

	// A rewrite only travels on the surfaces that read Request back after the
	// stage, and those are exactly the ones carrying no plugin.MetadataSurface.
	// Anywhere else the projection is one-way and the original body is
	// forwarded, so redacting would log a sanitization the provider never sees.
	_, projected := pctx.Metadata[plugin.MetadataSurface]
	if p.action == plugin.ActionRedact && !projected {
		p.redactRequest(ctx, pctx)
		return nil
	}

	if p.action == plugin.ActionLog {
		p.logRequest(ctx, pctx)
		return nil
	}

	for text := range plugin.RequestText(pctx.Request) {
		// The caller has gone; see plugin.RequestText for why this is not an error.
		if ctx.Err() != nil {
			return nil
		}
		name, found := p.detect(text)
		if !found {
			continue
		}
		logger.Ctx(ctx).Info("pii-redact: blocked request", "entity", name)
		pctx.NoteGuardrailMatch(plugin.ActionBlock)
		pctx.Reject = true
		// The entity TYPE, never the value: the caller needs to know what to
		// remove, and echoing the value back would put it in the error log of
		// every hop between here and them.
		pctx.Reason = "request blocked by content policy: " + name + " detected"
		if p.action == plugin.ActionRedact {
			// A verdict, not an error: the plugin reached a decision. Say why
			// the configured action did not apply, so the answer does not read
			// as an unexplained block on a surface the operator set to redact.
			pctx.Reason += "; content cannot be sanitized on this surface"
		}
		return nil
	}
	return nil
}

// Detect reports which built-in entity types occur in text, by name, sorted.
// It runs every built-in entity regardless of configuration, with the same
// patterns Execute enforces, and never returns matched text. No match is an
// empty, non-nil slice.
func Detect(text string) []string {
	names := []string{}
	for _, e := range builtinEntities {
		if e.matches(text) {
			names = append(names, e.name)
		}
	}
	slices.Sort(names)
	return names
}

// Close releases resources owned by the plugin.
func (p *PIIRedact) Close() error { return nil }

// logRequest records every entity type the request carries, once each, across
// every screenable field. block stops at the first match because one is a
// verdict; the record log keeps has to name everything block would deny, or
// it understates the policy it is sizing.
func (p *PIIRedact) logRequest(ctx context.Context, pctx *plugin.Context) {
	seen := make(map[string]bool, len(p.entities))
	for text := range plugin.RequestText(pctx.Request) {
		if ctx.Err() != nil {
			return
		}
		for _, e := range p.entities {
			if seen[e.name] || !e.matches(text) {
				continue
			}
			seen[e.name] = true
			logger.Ctx(ctx).Info("pii-redact: detected in request", "entity", e.name)
			pctx.NoteGuardrailMatch(plugin.ActionLog)
		}
	}
}

// redactRequest rewrites every screenable field in place — the same set
// plugin.RequestText screens, field for field.
//
// Content alone is not enough: a non-text part, a replayed tool call's
// arguments and reasoning content each leave no trace in it, so rewriting only
// Content would forward the value the plugin just claimed to remove. A field
// this misses is worse here than anywhere else, because block mode denies on it
// while redact mode reports a sanitization the provider never received.
func (p *PIIRedact) redactRequest(ctx context.Context, pctx *plugin.Context) {
	req := pctx.Request
	for i := range req.Messages {
		// The caller has gone, so the rewritten request will never be sent:
		// stop rather than rewrite the rest of it.
		if ctx.Err() != nil {
			return
		}
		msg := &req.Messages[i]
		msg.Content = p.redact(ctx, pctx, msg.Content)
		msg.ReasoningContent = p.redact(ctx, pctx, msg.ReasoningContent)
		for j := range msg.ContentParts {
			msg.ContentParts[j].Text = p.redact(ctx, pctx, msg.ContentParts[j].Text)
		}
		for j := range msg.ToolCalls {
			// The arguments are a JSON document, so the placeholder goes in
			// JSON-escaped: a quote or a backslash in it would otherwise turn a
			// valid argument object into one the provider cannot parse.
			msg.ToolCalls[j].Function.Arguments = p.redactWith(ctx, pctx, msg.ToolCalls[j].Function.Arguments, p.jsonPlaceholder)
		}
	}
}

// redact replaces every match with the configured placeholder as LITERAL text.
//
// ReplaceAllString would read the placeholder as a replacement template, where
// $0 stands for the whole match and $1 for the first group. A placeholder of
// "$0" therefore wrote the detected value straight back into the request while
// the plugin logged a redaction and let it through, and any placeholder
// carrying a dollar sign reached the provider as something other than what the
// operator wrote. Replacing through a function inserts the string as given.
func (p *PIIRedact) redact(ctx context.Context, pctx *plugin.Context, text string) string {
	return p.redactWith(ctx, pctx, text, p.placeholder)
}

func (p *PIIRedact) redactWith(ctx context.Context, pctx *plugin.Context, text, placeholder string) string {
	for _, e := range p.entities {
		if !e.matches(text) {
			continue
		}
		logger.Ctx(ctx).Info("pii-redact: redacted request", "entity", e.name)
		pctx.NoteGuardrailMatch(plugin.ActionRedact)
		text = e.re.ReplaceAllStringFunc(text, func(match string) string {
			if e.verify != nil && !e.verify(match) {
				return match
			}
			return placeholder
		})
	}
	return text
}

func (p *PIIRedact) detect(text string) (string, bool) {
	for _, e := range p.entities {
		if e.matches(text) {
			return e.name, true
		}
	}
	return "", false
}

// selectEntities resolves the entities selector. An ABSENT key selects every
// built-in entity. A key that is present says something about the selection, so
// a value that cannot express one — a scalar, a mapping — is a load error rather
// than a silent widening: an operator who asked for ssn and got all four also
// got credit_card, which matches any sixteen-digit order number and starts
// denying traffic they never opted into.
func selectEntities(raw any, present bool) ([]entity, error) {
	if !present {
		out := make([]entity, len(builtinEntities))
		copy(out, builtinEntities)
		return out, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("pii-redact: entities must be a list of entity names")
	}

	known := make([]string, len(builtinEntities))
	for i, e := range builtinEntities {
		known[i] = e.name
	}

	wanted := make(map[string]bool, len(list))
	for i, v := range list {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("pii-redact: entities[%d] must be a string", i)
		}
		name := strings.ToLower(strings.TrimSpace(s))
		// A name matching nothing selects nothing, which registers a plugin the
		// catalog reports as enabled and that screens no entity at all.
		if !slices.Contains(known, name) {
			return nil, fmt.Errorf("pii-redact: unrecognized entity %q: must be one of %q", s, known)
		}
		wanted[name] = true
	}

	out := make([]entity, 0, len(builtinEntities))
	for _, e := range builtinEntities {
		if wanted[e.name] {
			out = append(out, e)
		}
	}
	return out, nil
}
