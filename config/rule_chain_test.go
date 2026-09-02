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
		{"undeclared", Condition{Key: ConditionKeyModel, Value: "m", TargetKeys: []string{"a", "zzz"}}, "names no configured target"},
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

// TestValidateCondition_BoundedPredicates pins the request-shaped keys:
// stream and has_tools take "true"/"false"; metadata requires field; field
// is refused elsewhere.
func TestValidateCondition_BoundedPredicates(t *testing.T) {
	targets := []Target{{VirtualKey: "a"}}
	for _, tc := range []struct {
		name    string
		rule    Condition
		wantErr string
	}{
		{"user", Condition{Key: ConditionKeyUser, Value: "vip", TargetKey: "a"}, ""},
		{"stream true", Condition{Key: ConditionKeyStream, Value: "true", TargetKey: "a"}, ""},
		{"stream other", Condition{Key: ConditionKeyStream, Value: "yes", TargetKey: "a"}, "takes value"},
		{"has_tools false", Condition{Key: ConditionKeyHasTools, Value: "false", TargetKey: "a"}, ""},
		{"metadata", Condition{Key: ConditionKeyMetadata, Field: "tier", Value: "gold", TargetKey: "a"}, ""},
		{"metadata without field", Condition{Key: ConditionKeyMetadata, Value: "gold", TargetKey: "a"}, "requires field"},
		{"field elsewhere", Condition{Key: ConditionKeyModel, Field: "tier", Value: "m", TargetKey: "a"}, "field applies to key metadata only"},
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
}
