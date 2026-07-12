package strategies

import (
	"context"
	"strings"

	"github.com/ferro-labs/ai-gateway/providers"
)

// ConditionRule maps a condition to a target.
type ConditionRule struct {
	Key    string // "model", "model_prefix"
	Value  string
	Target Target
}

// Conditional routes requests based on matching conditions.
type Conditional struct {
	rules    []ConditionRule
	fallback Target
	targets  []Target
	lookup   ProviderLookup
}

// NewConditional creates a new conditional strategy.
// Rules are evaluated in order; the first match wins.
// The fallback target is used when no rule matches.
//
// targets seeds SelectTargets with the fallback so, absent WithRoutingTargets,
// streaming still selects the same fallback Execute routes to. WithRoutingTargets
// replaces it with the full ordered target list.
func NewConditional(rules []ConditionRule, fallback Target, lookup ProviderLookup) *Conditional {
	return &Conditional{
		rules:    rules,
		fallback: fallback,
		targets:  []Target{fallback},
		lookup:   lookup,
	}
}

// WithRoutingTargets records the full ordered target list. SelectTargets appends
// these as fallbacks after the matched condition target. Returns the receiver so
// callers can chain it after the constructor.
func (c *Conditional) WithRoutingTargets(targets []Target) *Conditional {
	c.targets = targets
	return c
}

// Execute routes the request to the provider whose SupportedModels includes the requested model.
func (c *Conditional) Execute(ctx context.Context, req providers.Request) (*providers.Response, error) {
	target := c.matchTarget(req)
	return dispatch(ctx, c.lookup, target, req, "provider not found")
}

// SelectTargets returns the first matching condition's target followed by every
// configured target as a fallback. With no match it returns the targets in
// declared order (targets[0] is the fallback used by Execute).
func (c *Conditional) SelectTargets(req providers.Request) ([]string, error) {
	keys := make([]string, 0, len(c.targets))
	for _, rule := range c.rules {
		if c.matches(rule, req) {
			keys = appendUniqueKey(keys, rule.Target.VirtualKey)
			break
		}
	}
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
	case "model":
		return req.Model == rule.Value
	case "model_prefix":
		return strings.HasPrefix(req.Model, rule.Value)
	default:
		return false
	}
}
