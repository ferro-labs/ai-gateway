// Package piiredact provides a pii-redact guardrail plugin that detects
// personally identifiable information and either denies the request or rewrites
// it with the values removed. Register it with a blank import:
//
//	_ "github.com/ferro-labs/ai-gateway/plugin/piiredact"
package piiredact

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/ferro-labs/ai-gateway/pkg/logger"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
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
}

// builtinEntities are the entity types recognised without configuration.
//
// The set is deliberately small and literal: each entry matches a well-known
// written form rather than guessing from context, because a false positive on
// this plugin costs a caller their request.
var builtinEntities = []entity{
	{"email", regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)},
	{"phone", regexp.MustCompile(`(\+1\d{10}|\(\d{3}\)\s?\d{3}-\d{4}|\d{3}[-.]\d{3}[-.]\d{4})`)},
	{"ssn", regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`)},
	{"credit_card", regexp.MustCompile(`\b\d{4}[\s\-]?\d{4}[\s\-]?\d{4}[\s\-]?\d{4}\b`)},
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
// The action set is deliberately just {block, redact} — unlike a filter, this
// plugin has no non-blocking observe mode: "warn" on a redactor would mean
// detecting PII and forwarding it anyway, which defeats the plugin's purpose.
type PIIRedact struct {
	entities    []entity
	action      string
	placeholder string
}

// Name returns the plugin identifier.
func (p *PIIRedact) Name() string { return "pii-redact" }

// Type returns the plugin lifecycle hook type.
func (p *PIIRedact) Type() plugin.PluginType { return plugin.TypeGuardrail }

// Init selects the entity set and the action.
func (p *PIIRedact) Init(config map[string]any) error {
	rawAction, err := plugin.StringSetting(config["action"], "action")
	if err != nil {
		return fmt.Errorf("pii-redact: %w", err)
	}
	action, err := plugin.NormalizeAction(rawAction, plugin.ActionBlock, plugin.ActionBlock, plugin.ActionRedact)
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
		p.redactRequest(ctx, pctx.Request)
		return nil
	}

	for text := range plugin.RequestText(pctx.Request) {
		if name, found := p.detect(text); found {
			logger.Ctx(ctx).Info("pii-redact: blocked request", "entity", name)
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
	}
	return nil
}

// Close releases resources owned by the plugin.
func (p *PIIRedact) Close() error { return nil }

// redactRequest rewrites every screenable field in place — the same set
// plugin.RequestText screens, field for field.
//
// Content alone is not enough: a non-text part, a replayed tool call's
// arguments and reasoning content each leave no trace in it, so rewriting only
// Content would forward the value the plugin just claimed to remove. A field
// this misses is worse here than anywhere else, because block mode denies on it
// while redact mode reports a sanitization the provider never received.
func (p *PIIRedact) redactRequest(ctx context.Context, req *providers.Request) {
	for i := range req.Messages {
		msg := &req.Messages[i]
		msg.Content = p.redact(ctx, msg.Content)
		msg.ReasoningContent = p.redact(ctx, msg.ReasoningContent)
		for j := range msg.ContentParts {
			msg.ContentParts[j].Text = p.redact(ctx, msg.ContentParts[j].Text)
		}
		for j := range msg.ToolCalls {
			msg.ToolCalls[j].Function.Arguments = p.redact(ctx, msg.ToolCalls[j].Function.Arguments)
		}
	}
}

func (p *PIIRedact) redact(ctx context.Context, text string) string {
	for _, e := range p.entities {
		if !e.re.MatchString(text) {
			continue
		}
		logger.Ctx(ctx).Info("pii-redact: redacted request", "entity", e.name)
		text = e.re.ReplaceAllString(text, p.placeholder)
	}
	return text
}

func (p *PIIRedact) detect(text string) (string, bool) {
	for _, e := range p.entities {
		if e.re.MatchString(text) {
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
