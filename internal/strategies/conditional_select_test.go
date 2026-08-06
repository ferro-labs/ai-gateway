package strategies

import (
	"testing"
)

// TestConditional_SelectTargets_FallbackWithoutRoutingTargets asserts that a
// Conditional built without WithRoutingTargets still surfaces its fallback
// target rather than an empty list.
func TestConditional_SelectTargets_FallbackWithoutRoutingTargets(t *testing.T) {
	rules := []ConditionRule{
		{Key: ConditionKeyModel, Value: "claude-3", Target: Target{VirtualKey: "other"}},
	}
	c := NewConditional(rules, Target{VirtualKey: "fb"})

	// req builds a gpt-4o request, which matches no rule.
	keys, err := c.SelectTargets(req("hi"))
	if err != nil {
		t.Fatal(err)
	}
	assertKeys(t, keys, "fb")
}

// TestConditional_SelectTargets_MatchThenFallback asserts a matched rule leads,
// with the fallback appended, even without WithRoutingTargets.
func TestConditional_SelectTargets_MatchThenFallback(t *testing.T) {
	rules := []ConditionRule{
		{Key: ConditionKeyModel, Value: "gpt-4o", Target: Target{VirtualKey: "other"}},
	}
	c := NewConditional(rules, Target{VirtualKey: "fb"})

	keys, err := c.SelectTargets(req("hi"))
	if err != nil {
		t.Fatal(err)
	}
	assertKeys(t, keys, "other", "fb")
}

// TestConditional_SelectTargets_UnmatchedModelLeadsWithFallback covers the one
// ordering where reading targets[0] and asking matchTarget disagree: an
// unmatched request must resolve to the DECLARED fallback, not to whatever
// WithRoutingTargets happens to list first.
func TestConditional_SelectTargets_UnmatchedModelLeadsWithFallback(t *testing.T) {
	rules := []ConditionRule{
		{Key: ConditionKeyModel, Value: "claude-3", Target: Target{VirtualKey: "first"}},
	}
	c := NewConditional(rules, Target{VirtualKey: "fb"}).
		WithRoutingTargets([]Target{{VirtualKey: "first"}, {VirtualKey: "fb"}})

	keys, err := c.SelectTargets(req("hi"))
	if err != nil {
		t.Fatal(err)
	}
	assertKeys(t, keys, "fb", "first")
}
