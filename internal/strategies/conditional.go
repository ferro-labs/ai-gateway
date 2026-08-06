package strategies

import (
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
)

// ConditionRule maps a condition to a target.
type ConditionRule struct {
	Key    string // one of ConditionKey*
	Value  string
	Target Target
}

// Conditional routes requests based on matching conditions.
type Conditional struct {
	rules    []ConditionRule
	fallback Target
	targets  []Target
}

// NewConditional creates a new conditional strategy.
// Rules are evaluated in order; the first match wins.
// The fallback target is used when no rule matches.
//
// targets seeds SelectTargets with the fallback; WithRoutingTargets replaces it
// with the full ordered target list.
func NewConditional(rules []ConditionRule, fallback Target) *Conditional {
	return &Conditional{
		rules:    rules,
		fallback: fallback,
		targets:  []Target{fallback},
	}
}

// WithRoutingTargets records the full ordered target list. SelectTargets appends
// these after the matched condition target. Returns the receiver so callers can
// chain it after the constructor.
func (c *Conditional) WithRoutingTargets(targets []Target) *Conditional {
	c.targets = targets
	return c
}

// SelectTargets returns the first matching condition's target — or the
// configured fallback when no rule matches — followed by every configured
// target.
//
// The rest of the list is not tried on failure: this mode commits to the target
// the rule named, so a failure there is the request's answer (see
// Strategy.SelectTargets). Two consequences an operator should expect from that:
//
// A request for a model the named target does not serve is answered
// model_not_found, even when a different configured target serves it. That is
// the rule doing what it was written to do — sending this traffic to this
// target is a decision, not a preference — so reaching the other target means
// writing a rule that names it. /v1/models answers the same way and does not
// advertise a model this mode would refuse, which is where the mismatch shows
// up before a request does.
//
// Circuit state is the one thing that does move the choice: a named target
// whose circuit is open is passed over for a healthy one, so a rule is
// preferred rather than exclusive while a backend is down, and traffic returns
// to it once its circuit closes.
//
// It leads with matchTarget rather than with targets[0] so an unmatched model
// resolves to the documented fallback target. targets[0] happens to be that
// fallback in the gateway's own wiring, but WithRoutingTargets takes any
// ordering and need not even include the fallback, so reading the head of the
// list is an assumption the type does not enforce; asking matchTarget is.
func (c *Conditional) SelectTargets(req providers.Request) ([]string, error) {
	keys := make([]string, 0, len(c.targets)+1)
	keys = appendUniqueKey(keys, c.matchTarget(req).VirtualKey)
	return appendRemainingTargetKeys(keys, c.targets), nil
}

func (c *Conditional) matchTarget(req providers.Request) Target {
	for _, rule := range c.rules {
		if c.matches(rule, req) {
			return rule.Target
		}
	}
	return c.fallback
}

func (c *Conditional) matches(rule ConditionRule, req providers.Request) bool {
	switch rule.Key {
	case ConditionKeyModel:
		return req.Model == rule.Value
	case ConditionKeyModelPrefix:
		return strings.HasPrefix(req.Model, rule.Value)
	default:
		// Unreachable for a validated config: an unknown key is a load error.
		// Kept because a false here is the only honest answer a matcher can give
		// about a rule it does not understand — the load-time rejection is what
		// stops that answer from silently rerouting traffic.
		return false
	}
}
