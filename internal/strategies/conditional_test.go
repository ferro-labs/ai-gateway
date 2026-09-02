package strategies

import (
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

func TestConditional_MatchesModel(t *testing.T) {
	rules := []ConditionRule{
		{Key: ConditionKeyModel, Value: "gpt-4o", Targets: []Target{{VirtualKey: "openai"}}},
		{Key: ConditionKeyModel, Value: "claude-3", Targets: []Target{{VirtualKey: "anthropic"}}},
	}
	c := NewConditional(rules, Target{VirtualKey: "openai"})

	assertLeadsWith(t, c, providers.Request{Model: "gpt-4o"}, "openai")
	assertLeadsWith(t, c, providers.Request{Model: "claude-3"}, "anthropic")
}

func TestConditional_ModelPrefix(t *testing.T) {
	rules := []ConditionRule{
		{Key: ConditionKeyModelPrefix, Value: "gpt-", Targets: []Target{{VirtualKey: "openai"}}},
		{Key: ConditionKeyModelPrefix, Value: "claude-", Targets: []Target{{VirtualKey: "anthropic"}}},
	}
	c := NewConditional(rules, Target{VirtualKey: "openai"})

	assertLeadsWith(t, c, providers.Request{Model: "claude-3-opus"}, "anthropic")
}

func TestConditional_Fallback(t *testing.T) {
	rules := []ConditionRule{
		{Key: ConditionKeyModel, Value: "gpt-4o", Targets: []Target{{VirtualKey: "other"}}},
	}
	c := NewConditional(rules, Target{VirtualKey: "fallback"})

	assertLeadsWith(t, c, providers.Request{Model: "unknown-model"}, "fallback")
}

// TestConditional_UnknownKeyNeverMatches pins the matcher's half of F16: a key
// it does not recognise matches nothing, so the rule's traffic goes to the
// fallback instead of the target the rule names. That is the only honest answer
// a matcher can give about a rule it cannot read — which is exactly why the key
// set is closed at config load, where the typo can still be reported. See
// TestValidateConfig_RejectsUnknownConditionKey.
func TestConditional_UnknownKeyNeverMatches(t *testing.T) {
	rules := []ConditionRule{
		{Key: "unknown_key", Value: "gpt-4o", Targets: []Target{{VirtualKey: "other"}}},
	}
	c := NewConditional(rules, Target{VirtualKey: "fb"})

	assertLeadsWith(t, c, providers.Request{Model: "gpt-4o"}, "fb")
}

func TestConditional_FirstRuleWins(t *testing.T) {
	// Both rules match "gpt-4o" (exact and prefix). First should win.
	rules := []ConditionRule{
		{Key: ConditionKeyModel, Value: "gpt-4o", Targets: []Target{{VirtualKey: "p1"}}},
		{Key: ConditionKeyModelPrefix, Value: "gpt-", Targets: []Target{{VirtualKey: "p2"}}},
	}
	c := NewConditional(rules, Target{VirtualKey: "p2"})

	assertLeadsWith(t, c, providers.Request{Model: "gpt-4o"}, "p1")
}

func TestConditional_NoRulesUsesFallback(t *testing.T) {
	c := NewConditional(nil, Target{VirtualKey: "fb"})
	assertLeadsWith(t, c, providers.Request{Model: "gpt-4o"}, "fb")
}

// assertLeadsWith checks the strategy's most-preferred target for req.
func assertLeadsWith(t *testing.T, s Strategy, req providers.Request, want string) {
	t.Helper()
	keys, err := s.SelectTargets(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) == 0 {
		t.Fatalf("SelectTargets returned no keys, want %q first", want)
	}
	if keys[0] != want {
		t.Errorf("SelectTargets leads with %q, want %q (full order %v)", keys[0], want, keys)
	}
}
