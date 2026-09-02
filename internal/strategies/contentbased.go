package strategies

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/ferro-labs/ai-gateway/providers"
)

// ContentConditionType identifies the matching logic used in a content-based routing rule.
type ContentConditionType string

const (
	// PromptContains matches when any user-role message content contains the
	// configured value (case-insensitive substring match).
	PromptContains ContentConditionType = "prompt_contains"

	// PromptNotContains matches when NO user-role message content contains the
	// configured value (case-insensitive). Useful for routing "everything that
	// is NOT about topic X" to a cheaper model.
	PromptNotContains ContentConditionType = "prompt_not_contains"

	// PromptRegex matches when any user-role message content matches the
	// configured Go regular expression pattern.
	PromptRegex ContentConditionType = "prompt_regex"
)

// ContentRule associates a content-matching condition with an ordered target
// chain (see ConditionRule.Targets).
type ContentRule struct {
	Type    ContentConditionType
	Value   string
	Targets []Target

	// re is the compiled regex for PromptRegex rules; nil for all other types.
	re *regexp.Regexp
}

// ContentBased routes requests based on the textual content of prompt messages.
//
// Rules are evaluated in declaration order; the first match wins. If no rule
// matches the request, traffic is sent to the fallback target. This enables
// prompt-aware model selection, e.g. routing code-related questions to a
// specialised coding model while falling back to a general model for
// everything else.
type ContentBased struct {
	rules    []ContentRule
	fallback Target
}

// NewContentBased creates a ContentBased strategy.
//
// Regex patterns in PromptRegex rules are compiled at construction time.
// Returns an error if any pattern is invalid. config.ValidateConfig compiles the
// same patterns at load, so for a validated config this cannot fail.
func NewContentBased(rules []ContentRule, fallback Target) (*ContentBased, error) {
	compiled := make([]ContentRule, len(rules))
	copy(compiled, rules)
	for i, r := range compiled {
		if r.Type == PromptRegex {
			re, err := regexp.Compile(r.Value)
			if err != nil {
				return nil, fmt.Errorf("content-based routing: invalid regex %q in rule %d: %w", r.Value, i, err)
			}
			compiled[i].re = re
		}
	}
	return &ContentBased{rules: compiled, fallback: fallback}, nil
}

// SelectTargets returns the first matching rule's chain, or the no-match
// fallback alone, with the boundary semantics Conditional.SelectTargets
// describes: the chain is walked, nothing outside it is ever substituted.
func (c *ContentBased) SelectTargets(req providers.Request) ([]string, error) {
	for _, rule := range c.rules {
		if c.matches(rule, req) {
			return targetKeys(rule.Targets), nil
		}
	}
	return []string{c.fallback.VirtualKey}, nil
}

func (c *ContentBased) matches(rule ContentRule, req providers.Request) bool {
	switch rule.Type {
	case PromptContains:
		return anyUserMessageContains(req, rule.Value)
	case PromptNotContains:
		return !anyUserMessageContains(req, rule.Value)
	case PromptRegex:
		return anyUserMessageMatchesRegex(req, rule.re)
	default:
		// Unreachable for a validated config — see Conditional.matches.
		return false
	}
}

// anyUserMessageContains returns true when at least one user-role message
// contains value as a case-insensitive substring.
func anyUserMessageContains(req providers.Request, value string) bool {
	lower := strings.ToLower(value)
	for _, msg := range req.Messages {
		if msg.Role == "user" && strings.Contains(strings.ToLower(msg.Content), lower) {
			return true
		}
	}
	return false
}

// anyUserMessageMatchesRegex returns true when at least one user-role message
// content matches the compiled regular expression.
func anyUserMessageMatchesRegex(req providers.Request, re *regexp.Regexp) bool {
	if re == nil {
		return false
	}
	for _, msg := range req.Messages {
		if msg.Role == "user" && re.MatchString(msg.Content) {
			return true
		}
	}
	return false
}
