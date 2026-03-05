// Package regexguard provides a configurable regex-based guardrail plugin.
package regexguard

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/ferro-labs/ai-gateway/plugin"
)

const (
	regexGuardApplyToInput  = "input"
	regexGuardApplyToOutput = "output"
	regexGuardApplyToBoth   = "both"
	regexGuardActionBlock   = "block"
	regexGuardActionWarn    = "warn"
	regexGuardActionLog     = "log"
)

func init() {
	plugin.RegisterFactory("regex-guard", func() plugin.Plugin {
		return &RegexGuard{}
	})
}

type rule struct {
	name     string
	compiled *regexp.Regexp
	applyTo  string
	action   string
}

// RegexGuard applies ordered regex rules to request/response content.
type RegexGuard struct {
	rules []rule
}

// Name returns the plugin identifier.
func (r *RegexGuard) Name() string { return "regex-guard" }

// Type returns the plugin lifecycle type.
func (r *RegexGuard) Type() plugin.PluginType { return plugin.TypeGuardrail }

// Init configures regex rules.
func (r *RegexGuard) Init(config map[string]interface{}) error {
	r.rules = nil
	rawRules, ok := config["rules"]
	if !ok {
		return nil
	}

	list, ok := rawRules.([]interface{})
	if !ok {
		return nil
	}

	for _, entry := range list {
		mapped, ok := entry.(map[string]interface{})
		if !ok {
			continue
		}

		name, _ := mapped["name"].(string)
		pattern, _ := mapped["pattern"].(string)
		name = strings.TrimSpace(name)
		pattern = strings.TrimSpace(pattern)
		if name == "" {
			return fmt.Errorf("regex-guard rule name is required")
		}
		if pattern == "" {
			return fmt.Errorf("regex-guard rule pattern is required for %s", name)
		}

		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return fmt.Errorf("compile regex-guard rule %s: %w", name, err)
		}

		parsedRule := rule{
			name:     name,
			compiled: compiled,
			applyTo:  parseApplyTo(mapped),
			action:   parseAction(mapped),
		}
		r.rules = append(r.rules, parsedRule)
	}

	return nil
}

// Execute evaluates rules for the current request stage.
func (r *RegexGuard) Execute(_ context.Context, pctx *plugin.Context) error {
	if pctx == nil {
		return nil
	}
	isAfterRequest := pctx.Response != nil

	for _, configuredRule := range r.rules {
		if isAfterRequest && configuredRule.applyTo == regexGuardApplyToInput {
			continue
		}
		if !isAfterRequest && configuredRule.applyTo == regexGuardApplyToOutput {
			continue
		}

		if !ruleMatches(configuredRule, pctx, isAfterRequest) {
			continue
		}

		if pctx.Metadata == nil {
			pctx.Metadata = make(map[string]interface{})
		}
		pctx.Metadata["regex_rule"] = configuredRule.name

		switch configuredRule.action {
		case regexGuardActionBlock:
			pctx.Reject = true
			pctx.Reason = "blocked by rule: " + configuredRule.name
			return nil
		case regexGuardActionWarn, regexGuardActionLog:
			// Continue evaluating remaining rules.
		}
	}

	return nil
}

func ruleMatches(configuredRule rule, pctx *plugin.Context, isAfterRequest bool) bool {
	if !isAfterRequest {
		if pctx.Request == nil {
			return false
		}
		for _, message := range pctx.Request.Messages {
			if configuredRule.compiled.MatchString(message.Content) {
				return true
			}
		}
		return false
	}

	for _, choice := range pctx.Response.Choices {
		if configuredRule.compiled.MatchString(choice.Message.Content) {
			return true
		}
	}
	return false
}

func parseApplyTo(config map[string]interface{}) string {
	raw, ok := config["apply_to"].(string)
	if !ok {
		return regexGuardApplyToInput
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case regexGuardApplyToInput, regexGuardApplyToOutput, regexGuardApplyToBoth:
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return regexGuardApplyToInput
	}
}

func parseAction(config map[string]interface{}) string {
	raw, ok := config["action"].(string)
	if !ok {
		return regexGuardActionBlock
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case regexGuardActionBlock, regexGuardActionWarn, regexGuardActionLog:
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return regexGuardActionBlock
	}
}
