package config

import (
	"strings"
	"testing"
)

// TestValidateRuleChain pins target_keys: exactly one of target_key and
// target_keys, every entry declared, no repeats.
func TestValidateRuleChain(t *testing.T) {
	targets := []Target{{VirtualKey: "a"}, {VirtualKey: "b"}}
	for _, tc := range []struct {
		name    string
		rule    Condition
		wantErr string
	}{
		{"chain", Condition{Key: ConditionKeyModel, Value: "m", TargetKeys: []string{"a", "b"}}, ""},
		{"sugar", Condition{Key: ConditionKeyModel, Value: "m", TargetKey: "a"}, ""},
		{"both", Condition{Key: ConditionKeyModel, Value: "m", TargetKey: "a", TargetKeys: []string{"b"}}, "not both"},
		{"neither", Condition{Key: ConditionKeyModel, Value: "m"}, "target_key is required"},
		{"undeclared", Condition{Key: ConditionKeyModel, Value: "m", TargetKeys: []string{"a", "zzz"}}, "does not name"},
		{"repeat", Condition{Key: ConditionKeyModel, Value: "m", TargetKeys: []string{"a", "a"}}, "repeats an earlier entry"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateConfig(Config{Strategy: StrategyConfig{Mode: ModeConditional, Conditions: []Condition{tc.rule}}, Targets: targets})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateConfig = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ValidateConfig = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
	if (Condition{TargetKey: "a"}).Chain()[0] != "a" || len(ContentCondition{TargetKeys: []string{"a", "b"}}.Chain()) != 2 {
		t.Fatal("Chain must return the sugar as a one-entry chain and the list as given")
	}
}
