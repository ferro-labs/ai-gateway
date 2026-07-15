package strategies

import "testing"

// TestConditional_SelectTargets_FallbackWithoutRoutingTargets asserts that a
// Conditional built without WithRoutingTargets still surfaces the fallback that
// Execute routes to, rather than an empty streaming list.
func TestConditional_SelectTargets_FallbackWithoutRoutingTargets(t *testing.T) {
	fb := &mockProvider{name: "fb", models: []string{"gpt-4o"}}
	other := &mockProvider{name: "other", models: []string{"gpt-4o"}}
	rules := []ConditionRule{
		{Key: "model", Value: "claude-3", Target: Target{VirtualKey: "other"}},
	}
	c := NewConditional(rules, Target{VirtualKey: "fb"}, newLookup(fb, other))

	// gpt-4o request matches no rule → Execute uses fallback fb; SelectTargets
	// must return it too, not nothing.
	keys, err := c.SelectTargets(req("hi"))
	if err != nil {
		t.Fatal(err)
	}
	assertKeys(t, keys, "fb")
}

// TestConditional_SelectTargets_MatchThenFallback asserts a matched rule leads,
// with the fallback appended, even without WithRoutingTargets.
func TestConditional_SelectTargets_MatchThenFallback(t *testing.T) {
	fb := &mockProvider{name: "fb", models: []string{"gpt-4o"}}
	other := &mockProvider{name: "other", models: []string{"gpt-4o"}}
	rules := []ConditionRule{
		{Key: "model", Value: "gpt-4o", Target: Target{VirtualKey: "other"}},
	}
	c := NewConditional(rules, Target{VirtualKey: "fb"}, newLookup(fb, other))

	keys, err := c.SelectTargets(req("hi"))
	if err != nil {
		t.Fatal(err)
	}
	assertKeys(t, keys, "other", "fb")
}
