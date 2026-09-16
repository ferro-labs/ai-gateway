// Package regexguard provides a regex-guard guardrail plugin that rejects or
// flags content matching configured regular expressions. Register it with a
// blank import:
//
//	_ "github.com/ferro-labs/ai-gateway/plugin/regexguard"
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

const actionBlock = "block"

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
	defaultAction := actionBlock
	if a, ok := config["action"].(string); ok {
		normalized, err := plugin.NormalizeAction(a, actionBlock, plugin.ActionBlock, plugin.ActionWarn, plugin.ActionLog)
		if err != nil {
			return fmt.Errorf("regex-guard: action: %w", err)
		}
		defaultAction = normalized
	}

	raw, ok := config["rules"].([]any)
	if !ok {
		return nil
	}

	for i, entry := range raw {
		mapped, ok := entry.(map[string]any)
		if !ok {
			return fmt.Errorf("regex-guard: rules[%d] must be an object", i)
		}

		pattern, _ := mapped["pattern"].(string)
		if strings.TrimSpace(pattern) == "" {
			return fmt.Errorf("regex-guard: rules[%d] requires a non-empty pattern", i)
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return fmt.Errorf("regex-guard: rules[%d]: %w", i, err)
		}

		name, _ := mapped["name"].(string)
		if strings.TrimSpace(name) == "" {
			name = fmt.Sprintf("rule_%d", i+1)
		}

		action := defaultAction
		if a, ok := mapped["action"].(string); ok {
			normalized, err := plugin.NormalizeAction(a, defaultAction, plugin.ActionBlock, plugin.ActionWarn, plugin.ActionLog)
			if err != nil {
				return fmt.Errorf("regex-guard: rules[%d]: action: %w", i, err)
			}
			action = normalized
		}

		applyTo, _ := mapped["apply_to"].(string)

		g.rules = append(g.rules, rule{
			name:    name,
			re:      re,
			applyTo: normalizeApplyTo(applyTo),
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
		if r.action != actionBlock {
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

func normalizeApplyTo(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case applyToOutput:
		return applyToOutput
	case applyToBoth:
		return applyToBoth
	default:
		return applyToInput
	}
}
