package strategies

import (
	"context"
	"fmt"
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
	lookup   ProviderLookup
}

// NewConditional creates a new conditional strategy.
// Rules are evaluated in order; the first match wins.
// The fallback target is used when no rule matches.
func NewConditional(rules []ConditionRule, fallback Target, lookup ProviderLookup) *Conditional {
	return &Conditional{
		rules:    rules,
		fallback: fallback,
		lookup:   lookup,
	}
}

// Execute routes the request to the provider whose SupportedModels includes the requested model.
func (c *Conditional) Execute(ctx context.Context, req providers.Request) (*providers.Response, error) {
	target := c.matchTarget(req)

	p, ok := c.lookup(target.VirtualKey)
	if !ok {
		return nil, fmt.Errorf("provider not found: %s", target.VirtualKey)
	}
	return p.Complete(ctx, req)
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
