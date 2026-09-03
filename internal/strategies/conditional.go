package strategies

import (
	"strconv"
	"strings"

	"github.com/ferro-labs/ai-gateway/providers"
)

// ConditionKey* are the accepted values for ConditionRule.Key. The set is
// closed: matches below has no dynamic arm, so a key outside it can only be a
// typo. config.ValidateConfig rejects one at load against config.ConditionKeys,
// and TestConditionKeysMatchConfig keeps the two lists from drifting apart.
const (
	ConditionKeyModel       = "model"
	ConditionKeyModelPrefix = "model_prefix"
	ConditionKeyUser        = "user"
	ConditionKeyStream      = "stream"
	ConditionKeyHasTools    = "has_tools"
	ConditionKeyMetadata    = "metadata"
)

// ConditionRule maps a condition to an ordered target chain.
type ConditionRule struct {
	Key   string // one of ConditionKey*
	Value string
	// Field names the metadata entry a ConditionKeyMetadata rule reads.
	Field string
	// Targets is the chain the rule routes to, most preferred first. A
	// one-entry chain is an exact target.
	Targets []Target
}

// Conditional routes requests based on matching conditions.
type Conditional struct {
	rules    []ConditionRule
	fallback Target
}

// NewConditional creates a new conditional strategy.
// Rules are evaluated in order; the first match wins.
// The fallback target is used when no rule matches.
func NewConditional(rules []ConditionRule, fallback Target) *Conditional {
	return &Conditional{rules: rules, fallback: fallback}
}

// SelectTargets returns the first matching rule's chain, or the configured
// fallback alone when no rule matches — and nothing else.
//
// The chain IS the candidate list: the pipeline walks it in order, advancing
// only on a failover-safe failure and skipping a member whose circuit is open,
// that is saturated, or that is parked after a 429, exactly as it walks a
// pool. What it never does is substitute a target outside the chain. A rule
// that names one target is therefore exact: if that target cannot serve the
// request — wrong model, no capability for the surface, circuit open — the
// answer is the corresponding gateway error, not a sibling from the wider
// pool. Sending this traffic to these targets is a decision the rule made;
// reaching another target means writing a rule that names it. /v1/models
// answers the same way and does not advertise a model this mode would refuse.
func (c *Conditional) SelectTargets(req providers.Request) ([]string, error) {
	return targetKeys(c.matchChain(req)), nil
}

func (c *Conditional) matchChain(req providers.Request) []Target {
	for _, rule := range c.rules {
		if c.matches(rule, req) {
			return rule.Targets
		}
	}
	return []Target{c.fallback}
}

func (c *Conditional) matches(rule ConditionRule, req providers.Request) bool {
	switch rule.Key {
	case ConditionKeyModel:
		return req.Model == rule.Value
	case ConditionKeyModelPrefix:
		return strings.HasPrefix(req.Model, rule.Value)
	case ConditionKeyUser:
		return req.User != "" && req.User == rule.Value
	case ConditionKeyStream:
		return strconv.FormatBool(req.Stream) == rule.Value
	case ConditionKeyHasTools:
		return strconv.FormatBool(len(req.Tools) > 0) == rule.Value
	case ConditionKeyMetadata:
		got, ok := req.RoutingMetadata[rule.Field]
		return ok && got == rule.Value
	default:
		// Unreachable for a validated config: an unknown key is a load error.
		// Kept because a false here is the only honest answer a matcher can give
		// about a rule it does not understand — the load-time rejection is what
		// stops that answer from silently rerouting traffic.
		return false
	}
}
