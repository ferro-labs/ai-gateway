package strategies

import (
	"testing"
)

// TestConditional_SelectTargets_Fallback asserts an unmatched request resolves
// to the fallback target rather than an empty list.
func TestConditional_SelectTargets_Fallback(t *testing.T) {
	rules := []ConditionRule{
		{Key: ConditionKeyModel, Value: "claude-3", Targets: []Target{{VirtualKey: "other"}}},
	}
	c := NewConditional(rules, Target{VirtualKey: "fb"})

	// req builds a gpt-4o request, which matches no rule.
	keys, err := c.SelectTargets(req("hi"))
	if err != nil {
		t.Fatal(err)
	}
	assertKeys(t, keys, "fb")
}

// TestConditional_SelectTargets_MatchIsTheWholeAnswer asserts a matched rule's
// chain is the candidate list — the fallback is not appended, so nothing
// outside the chain can be substituted.
func TestConditional_SelectTargets_MatchIsTheWholeAnswer(t *testing.T) {
	rules := []ConditionRule{
		{Key: ConditionKeyModel, Value: "gpt-4o", Targets: []Target{{VirtualKey: "other"}}},
	}
	c := NewConditional(rules, Target{VirtualKey: "fb"})

	keys, err := c.SelectTargets(req("hi"))
	if err != nil {
		t.Fatal(err)
	}
	assertKeys(t, keys, "other")
}

// TestConditional_SelectTargets_UnmatchedModelIsTheFallbackAlone: an unmatched
// request resolves to the declared fallback and nothing else.
func TestConditional_SelectTargets_UnmatchedModelIsTheFallbackAlone(t *testing.T) {
	rules := []ConditionRule{
		{Key: ConditionKeyModel, Value: "claude-3", Targets: []Target{{VirtualKey: "first"}}},
	}
	c := NewConditional(rules, Target{VirtualKey: "fb"})

	keys, err := c.SelectTargets(req("hi"))
	if err != nil {
		t.Fatal(err)
	}
	assertKeys(t, keys, "fb")
}
