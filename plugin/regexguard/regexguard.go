// Package regexguard provides a regex-guard guardrail plugin that rejects or
// flags content matching configured regular expressions. Register it with a
// blank import:
//
//	_ "github.com/ferro-labs/ai-gateway/plugin/regexguard"
//
// A rule's apply_to and the plugin entry's stage are two separate settings and
// both must agree: a rule with apply_to "output" or "both" only screens the
// response when this plugin is ALSO listed at after_request, because one
// plugins[] entry registers one stage. A rule that names neither direction
// screens the request.
//
// Only action "block" rejects. Under "warn" and "log" the match is recorded by
// rule name and the content is forwarded anyway.
package regexguard

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/ferro-labs/ai-gateway/pkg/logger"
	"github.com/ferro-labs/ai-gateway/plugin"
)

func init() {
	plugin.RegisterFactory("regex-guard", func() plugin.Plugin {
		return &RegexGuard{}
	})
}

// Apply-to scopes. A rule screens the request, the response, or both.
const (
	applyToInput  = "input"
	applyToOutput = "output"
	applyToBoth   = "both"
)

// rule is one compiled regex-guard rule.
type rule struct {
	name    string
	re      *regexp.Regexp
	applyTo string
	action  string
}

// RegexGuard screens content against named regular expressions.
//
// Patterns are compiled once at Init and an uncompilable pattern fails the
// load. The alternative — compiling per request and skipping on error — turns a
// typo into a guardrail that matches nothing for the life of the deployment
// while reporting itself as enabled.
//
// Matching uses Go's RE2 engine: linear time, no backtracking, program size
// capped at compile. An operator-supplied pattern cannot mount a ReDoS.
type RegexGuard struct {
	rules []rule
}

// Name returns the plugin identifier.
func (g *RegexGuard) Name() string { return "regex-guard" }

// Type returns the plugin lifecycle hook type.
func (g *RegexGuard) Type() plugin.PluginType { return plugin.TypeGuardrail }

// Init compiles the configured rules.
func (g *RegexGuard) Init(config map[string]any) error {
	rawAction, err := plugin.StringSetting(config["action"], "action")
	if err != nil {
		return fmt.Errorf("regex-guard: %w", err)
	}
	defaultAction, err := plugin.NormalizeAction(rawAction, plugin.ActionBlock, plugin.ActionBlock, plugin.ActionWarn, plugin.ActionLog)
	if err != nil {
		return fmt.Errorf("regex-guard: action: %w", err)
	}

	rules, present := config["rules"]
	if !present {
		return nil
	}
	// Present but not a sequence — a rules block written as a mapping. Reading
	// that as "no rules" yields a plugin the catalog reports as enabled and
	// that screens nothing.
	raw, ok := rules.([]any)
	if !ok {
		return fmt.Errorf("regex-guard: rules must be a list of rule objects")
	}
	// Present and empty is a different statement from absent, and only one of
	// them is a configuration. Absent means the plugin was not set up; an empty
	// list is an operator who set out to name rules, and accepting it yields the
	// same enabled-and-inert guardrail a mapping would.
	if len(raw) == 0 {
		return fmt.Errorf("regex-guard: rules is empty: omit the key to disable the plugin, or name at least one rule")
	}

	for i, entry := range raw {
		mapped, ok := entry.(map[string]any)
		if !ok {
			return fmt.Errorf("regex-guard: rules[%d] must be an object", i)
		}

		pattern, err := plugin.StringSetting(mapped["pattern"], "pattern")
		if err != nil {
			return fmt.Errorf("regex-guard: rules[%d]: %w", i, err)
		}
		if strings.TrimSpace(pattern) == "" {
			return fmt.Errorf("regex-guard: rules[%d] requires a non-empty pattern", i)
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return fmt.Errorf("regex-guard: rules[%d]: %w", i, err)
		}

		name, err := plugin.StringSetting(mapped["name"], "name")
		if err != nil {
			return fmt.Errorf("regex-guard: rules[%d]: %w", i, err)
		}
		if strings.TrimSpace(name) == "" {
			name = fmt.Sprintf("rule_%d", i+1)
		}

		rawRuleAction, err := plugin.StringSetting(mapped["action"], "action")
		if err != nil {
			return fmt.Errorf("regex-guard: rules[%d]: %w", i, err)
		}
		action, err := plugin.NormalizeAction(rawRuleAction, defaultAction, plugin.ActionBlock, plugin.ActionWarn, plugin.ActionLog)
		if err != nil {
			return fmt.Errorf("regex-guard: rules[%d]: action: %w", i, err)
		}

		rawApplyTo, err := plugin.StringSetting(mapped["apply_to"], "apply_to")
		if err != nil {
			return fmt.Errorf("regex-guard: rules[%d]: %w", i, err)
		}
		applyTo, err := normalizeApplyTo(rawApplyTo)
		if err != nil {
			return fmt.Errorf("regex-guard: rules[%d]: %w", i, err)
		}

		g.rules = append(g.rules, rule{
			name:    name,
			re:      re,
			applyTo: applyTo,
			action:  action,
		})
	}
	return nil
}

// Execute screens the request at before_request and the response at
// after_request. The first matching block rule short-circuits.
func (g *RegexGuard) Execute(ctx context.Context, pctx *plugin.Context) error {
	if len(g.rules) == 0 {
		return nil
	}

	if pctx.Stage == plugin.StageAfterRequest {
		for text := range plugin.ResponseText(pctx.Response) {
			if ctx.Err() != nil {
				return nil
			}
			if g.screen(ctx, pctx, text, true, "response") {
				return nil
			}
		}
		return nil
	}

	if plugin.RejectUninspectable(pctx) {
		return nil
	}
	for text := range plugin.RequestText(pctx.Request) {
		// The caller has gone: stop matching rather than walk the rest of a body
		// nobody is waiting for. Returning nil and not the context's error is
		// the whole point — an error from Execute means the plugin broke, which
		// the gateway answers 500 and the target's circuit breaker counts as a
		// fault. A caller hanging up is neither.
		if ctx.Err() != nil {
			return nil
		}
		if g.screen(ctx, pctx, text, false, "request") {
			return nil
		}
	}
	return nil
}

// screen tests one piece of content against every rule covering this direction
// and reports whether the content was blocked.
//
// The matched text and the pattern are logged server-side only. Neither reaches
// pctx.Reason: a reason that quoted either would let a caller reconstruct the
// operator's policy one probe at a time.
func (g *RegexGuard) screen(ctx context.Context, pctx *plugin.Context, content string, isOutput bool, subject string) bool {
	for _, r := range g.rules {
		if !r.applies(isOutput) || !r.re.MatchString(content) {
			continue
		}
		logger.Ctx(ctx).Info("regex-guard: matched "+subject,
			"rule", r.name, "action", r.action)
		if r.action != plugin.ActionBlock {
			continue
		}
		pctx.Reject = true
		pctx.Reason = subject + " blocked by content policy"
		return true
	}
	return false
}

// Close releases resources owned by the plugin.
func (g *RegexGuard) Close() error { return nil }

func (r rule) applies(isOutput bool) bool {
	switch r.applyTo {
	case applyToOutput:
		return isOutput
	case applyToBoth:
		return true
	default:
		return !isOutput
	}
}

// normalizeApplyTo canonicalises a rule's scope and rejects anything outside
// the three.
//
// An unrecognised scope is not degraded to input. Input screening is not a
// superset of output screening, so an operator who wrote "outupt" meaning to
// screen the model's answer would get a rule that screens the prompt instead —
// a different rule, silently substituted. An ABSENT scope is not a
// misspelling: it means the key was not set, so it takes the input default.
func normalizeApplyTo(raw string) (string, error) {
	switch scope := strings.ToLower(strings.TrimSpace(raw)); scope {
	case "":
		return applyToInput, nil
	case applyToInput, applyToOutput, applyToBoth:
		return scope, nil
	default:
		return "", fmt.Errorf("unrecognized apply_to %q: must be one of %q", raw,
			[]string{applyToInput, applyToOutput, applyToBoth})
	}
}
