package config

import (
	"strings"
	"testing"
)

// twoTargets is the target list every case below references.
func twoTargets() []Target {
	return []Target{{VirtualKey: "openai", Weight: 1}, {VirtualKey: "groq", Weight: 1}}
}

func TestValidateStrategy(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string // substring; empty means the config must be accepted
	}{
		// ── F15 · a negative ab-test weight used to start clean and 500 every
		// request, with the reason in no log line and no response body.
		{
			name: "ab-test negative weight",
			cfg: Config{
				Strategy: StrategyConfig{Mode: ModeABTest, ABVariants: []ABVariantConfig{
					{TargetKey: "openai", Weight: -1, Label: "control"},
					{TargetKey: "groq", Weight: 20, Label: "challenger"},
				}},
				Targets: twoTargets(),
			},
			wantErr: "negative weight",
		},
		{
			name: "ab-test all weights zero",
			cfg: Config{
				Strategy: StrategyConfig{Mode: ModeABTest, ABVariants: []ABVariantConfig{
					{TargetKey: "openai", Weight: 0, Label: "control"},
					{TargetKey: "groq", Weight: 0, Label: "challenger"},
				}},
				Targets: twoTargets(),
			},
			wantErr: "sum to zero",
		},
		{
			name: "ab-test one drained variant is legal",
			cfg: Config{
				Strategy: StrategyConfig{Mode: ModeABTest, ABVariants: []ABVariantConfig{
					{TargetKey: "openai", Weight: 0, Label: "control"},
					{TargetKey: "groq", Weight: 20, Label: "challenger"},
				}},
				Targets: twoTargets(),
			},
		},
		{
			name: "ab-test duplicate target",
			cfg: Config{
				Strategy: StrategyConfig{Mode: ModeABTest, ABVariants: []ABVariantConfig{
					{TargetKey: "openai", Weight: 80, Label: "control"},
					{TargetKey: "openai", Weight: 20, Label: "challenger"},
				}},
				Targets: twoTargets(),
			},
			wantErr: `ab_variants[1].target_key "openai" duplicates ab_variants[0].target_key`,
		},

		// ── F16 · a typo in a condition key or content-condition type used to be
		// accepted and route 100% of that rule's traffic to targets[0], HTTP 200,
		// no warning.
		{
			name: "conditional unknown key",
			cfg: Config{
				Strategy: StrategyConfig{Mode: ModeConditional, Conditions: []Condition{
					{Key: "model_prefx", Value: "gpt-", TargetKey: "groq"},
				}},
				Targets: twoTargets(),
			},
			wantErr: `unknown key "model_prefx"`,
		},
		{
			name: "conditional known keys",
			cfg: Config{
				Strategy: StrategyConfig{Mode: ModeConditional, Conditions: []Condition{
					{Key: ConditionKeyModel, Value: "gpt-4o", TargetKey: "openai"},
					{Key: ConditionKeyModelPrefix, Value: "llama", TargetKey: "groq"},
				}},
				Targets: twoTargets(),
			},
		},
		{
			name: "content-based unknown type",
			cfg: Config{
				Strategy: StrategyConfig{Mode: ModeContentBased, ContentConditions: []ContentCondition{
					{Type: "prompt_contain", Value: "python", TargetKey: "groq"},
				}},
				Targets: twoTargets(),
			},
			wantErr: `unknown type "prompt_contain"`,
		},
		{
			name: "content-based invalid regex",
			cfg: Config{
				Strategy: StrategyConfig{Mode: ModeContentBased, ContentConditions: []ContentCondition{
					{Type: ContentConditionPromptRegex, Value: "[invalid", TargetKey: "groq"},
				}},
				Targets: twoTargets(),
			},
			wantErr: "invalid regex",
		},

		// ── F17 · a mistyped target_key used to 404 one surface and silently
		// serve the other from the wrong provider.
		{
			name: "content-based target_key typo",
			cfg: Config{
				Strategy: StrategyConfig{Mode: ModeContentBased, ContentConditions: []ContentCondition{
					{Type: ContentConditionPromptContains, Value: "python", TargetKey: "grok"},
				}},
				Targets: twoTargets(),
			},
			wantErr: `target_key "grok" names no configured target`,
		},
		{
			name: "conditional target_key typo",
			cfg: Config{
				Strategy: StrategyConfig{Mode: ModeConditional, Conditions: []Condition{
					{Key: ConditionKeyModel, Value: "gpt-4o", TargetKey: "openai-eu"},
				}},
				Targets: twoTargets(),
			},
			wantErr: `names no configured target`,
		},
		{
			name: "ab-test target_key typo",
			cfg: Config{
				Strategy: StrategyConfig{Mode: ModeABTest, ABVariants: []ABVariantConfig{
					{TargetKey: "openai", Weight: 80, Label: "control"},
					{TargetKey: "grok", Weight: 20, Label: "challenger"},
				}},
				Targets: twoTargets(),
			},
			wantErr: `names no configured target`,
		},
		{
			name: "empty target_key",
			cfg: Config{
				Strategy: StrategyConfig{Mode: ModeConditional, Conditions: []Condition{
					{Key: ConditionKeyModel, Value: "gpt-4o"},
				}},
				Targets: twoTargets(),
			},
			wantErr: "target_key is required",
		},

		// ── F14 · weight: 0 must mean zero, and an all-zero set must not load.
		{
			name: "loadbalance one drained target is legal",
			cfg: Config{
				Strategy: StrategyConfig{Mode: ModeLoadBalance},
				Targets:  []Target{{VirtualKey: "openai", Weight: 0}, {VirtualKey: "groq", Weight: 100}},
			},
		},
		{
			name: "loadbalance all weights zero",
			cfg: Config{
				Strategy: StrategyConfig{Mode: ModeLoadBalance},
				Targets:  []Target{{VirtualKey: "openai"}, {VirtualKey: "groq"}},
			},
			wantErr: "sum to zero",
		},
		{
			name: "loadbalance negative weight",
			cfg: Config{
				Strategy: StrategyConfig{Mode: ModeLoadBalance},
				Targets:  []Target{{VirtualKey: "openai", Weight: -1}, {VirtualKey: "groq", Weight: 1}},
			},
			wantErr: "negative weight",
		},
		// Weight is only read under loadbalance, so an omitted weight elsewhere
		// must stay legal — otherwise the commonest config in the examples fails.
		{
			name: "omitted weights are fine outside loadbalance",
			cfg: Config{
				Strategy: StrategyConfig{Mode: ModeFallback},
				Targets:  []Target{{VirtualKey: "openai"}, {VirtualKey: "groq"}},
			},
		},

		// ── mode and per-mode required blocks.
		{
			name:    "unknown mode",
			cfg:     Config{Strategy: StrategyConfig{Mode: "roundrobin"}, Targets: twoTargets()},
			wantErr: "unknown strategy mode",
		},
		{
			name:    "conditional with no conditions",
			cfg:     Config{Strategy: StrategyConfig{Mode: ModeConditional}, Targets: twoTargets()},
			wantErr: "at least one condition",
		},
		{
			name:    "content-based with no conditions",
			cfg:     Config{Strategy: StrategyConfig{Mode: ModeContentBased}, Targets: twoTargets()},
			wantErr: "at least one content_condition",
		},
		{
			name:    "ab-test with no variants",
			cfg:     Config{Strategy: StrategyConfig{Mode: ModeABTest}, Targets: twoTargets()},
			wantErr: "at least one ab_variant",
		},
		{
			name: "cost-optimized bad unpriced_strategy",
			cfg: Config{
				Strategy: StrategyConfig{Mode: ModeCostOptimized, UnpricedStrategy: "cheapest"},
				Targets:  twoTargets(),
			},
			wantErr: "unpriced_strategy must be one of",
		},
		{
			name: "cost-optimized valid unpriced_strategy",
			cfg: Config{
				Strategy: StrategyConfig{Mode: ModeCostOptimized, UnpricedStrategy: UnpricedStrategySkip},
				Targets:  twoTargets(),
			},
		},
		{
			name: "empty mode normalizes to single",
			cfg:  Config{Targets: twoTargets()},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateConfig(tt.cfg)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateConfig = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateConfig = nil, want an error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateConfig = %v, want an error containing %q", err, tt.wantErr)
			}
		})
	}
}

// TestValidateStrategy_ErrorNamesTheOffender: a rejection an operator cannot act
// on is barely better than the silence it replaces, so the message has to carry
// the rule's position, the bad value, and what was allowed instead.
func TestValidateStrategy_ErrorNamesTheOffender(t *testing.T) {
	err := ValidateConfig(Config{
		Strategy: StrategyConfig{Mode: ModeConditional, Conditions: []Condition{
			{Key: ConditionKeyModel, Value: "gpt-4o", TargetKey: "openai"},
			{Key: "model_prefx", Value: "gpt-", TargetKey: "groq"},
		}},
		Targets: twoTargets(),
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"conditions[1]", "model_prefx", "model_prefix"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}
