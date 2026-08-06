package aigateway

import (
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/internal/latency"
	"github.com/ferro-labs/ai-gateway/internal/strategies"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/providers"
)

// strategyCorpus is one config per shape that either validation or construction
// has an opinion about. It is deliberately a mixture of valid and invalid: the
// test below asserts a RELATION between the two answers, so it needs cases that
// land on both sides.
func strategyCorpus() []struct {
	name string
	cfg  config.Config
} {
	targets := []config.Target{{VirtualKey: "openai", Weight: 1}, {VirtualKey: "groq", Weight: 1}}
	return []struct {
		name string
		cfg  config.Config
	}{
		{"single", config.Config{Strategy: config.StrategyConfig{Mode: config.ModeSingle}, Targets: targets}},
		{"mode omitted", config.Config{Targets: targets}},
		{"fallback", config.Config{Strategy: config.StrategyConfig{Mode: config.ModeFallback}, Targets: targets}},
		{"loadbalance", config.Config{Strategy: config.StrategyConfig{Mode: config.ModeLoadBalance}, Targets: targets}},
		{"loadbalance all zero", config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeLoadBalance},
			Targets:  []config.Target{{VirtualKey: "openai"}, {VirtualKey: "groq"}},
		}},
		{"loadbalance negative", config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeLoadBalance},
			Targets:  []config.Target{{VirtualKey: "openai", Weight: -1}, {VirtualKey: "groq", Weight: 1}},
		}},
		{"least-latency", config.Config{Strategy: config.StrategyConfig{Mode: config.ModeLatency}, Targets: targets}},
		{"cost-optimized", config.Config{Strategy: config.StrategyConfig{Mode: config.ModeCostOptimized}, Targets: targets}},
		{"cost-optimized skip", config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeCostOptimized, UnpricedStrategy: "skip"},
			Targets:  targets,
		}},
		{"cost-optimized bad unpriced", config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeCostOptimized, UnpricedStrategy: "cheapest"},
			Targets:  targets,
		}},
		{"conditional", config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeConditional, Conditions: []config.Condition{
				{Key: config.ConditionKeyModelPrefix, Value: "gpt-", TargetKey: "openai"},
			}},
			Targets: targets,
		}},
		{"conditional no conditions", config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeConditional}, Targets: targets,
		}},
		{"conditional unknown key", config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeConditional, Conditions: []config.Condition{
				{Key: "model_prefx", Value: "gpt-", TargetKey: "groq"},
			}},
			Targets: targets,
		}},
		{"conditional unknown target", config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeConditional, Conditions: []config.Condition{
				{Key: config.ConditionKeyModel, Value: "gpt-4o", TargetKey: "nope"},
			}},
			Targets: targets,
		}},
		{"content-based", config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeContentBased, ContentConditions: []config.ContentCondition{
				{Type: config.ContentConditionPromptRegex, Value: "(?i)python", TargetKey: "groq"},
			}},
			Targets: targets,
		}},
		{"content-based no conditions", config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeContentBased}, Targets: targets,
		}},
		{"content-based bad regex", config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeContentBased, ContentConditions: []config.ContentCondition{
				{Type: config.ContentConditionPromptRegex, Value: "[invalid", TargetKey: "groq"},
			}},
			Targets: targets,
		}},
		{"content-based unknown type", config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeContentBased, ContentConditions: []config.ContentCondition{
				{Type: "prompt_contain", Value: "python", TargetKey: "groq"},
			}},
			Targets: targets,
		}},
		{"ab-test", config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeABTest, ABVariants: []config.ABVariantConfig{
				{TargetKey: "openai", Weight: 80, Label: "control"},
				{TargetKey: "groq", Weight: 20, Label: "challenger"},
			}},
			Targets: targets,
		}},
		{"ab-test drained variant", config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeABTest, ABVariants: []config.ABVariantConfig{
				{TargetKey: "openai", Weight: 0, Label: "control"},
				{TargetKey: "groq", Weight: 20, Label: "challenger"},
			}},
			Targets: targets,
		}},
		{"ab-test negative weight", config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeABTest, ABVariants: []config.ABVariantConfig{
				{TargetKey: "openai", Weight: -1, Label: "control"},
				{TargetKey: "groq", Weight: 20, Label: "challenger"},
			}},
			Targets: targets,
		}},
		{"ab-test all zero", config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeABTest, ABVariants: []config.ABVariantConfig{
				{TargetKey: "openai", Weight: 0, Label: "control"},
				{TargetKey: "groq", Weight: 0, Label: "challenger"},
			}},
			Targets: targets,
		}},
		{"ab-test no variants", config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeABTest}, Targets: targets,
		}},
		{"unknown mode", config.Config{Strategy: config.StrategyConfig{Mode: "roundrobin"}, Targets: targets}},
		{"no targets", config.Config{Strategy: config.StrategyConfig{Mode: config.ModeFallback}}},
	}
}

func corpusTargets(cfg config.Config) []strategies.Target {
	out := make([]strategies.Target, len(cfg.Targets))
	for i, t := range cfg.Targets {
		out[i] = strategies.Target{VirtualKey: t.VirtualKey, Weight: t.Weight}
	}
	return out
}

// TestValidateConfigCoversStrategyConstruction is the guard that keeps F15 from
// coming back in another shape.
//
// A strategy is built lazily, on the first request that needs one. That laziness
// is not incidental — the provider lookup snapshots the registry, and providers
// are registered AFTER New returns, so building eagerly in New would capture an
// empty map. What makes lazy construction safe is this relation: if
// ValidateConfig accepts a config, buildStrategy must succeed on it. Break the
// relation and a config that passes `ferrogw validate`, boots, and reports ready
// starts answering 500 to every request — which is exactly what a negative
// ab_variants weight did, with the reason in no log line and no response body.
func TestValidateConfigCoversStrategyConstruction(t *testing.T) {
	lookup := func(string) (providers.Provider, bool) { return nil, false }

	for _, tc := range strategyCorpus() {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			cfg.Normalize()
			validErr := config.ValidateConfig(cfg)
			_, buildErr := buildStrategy(cfg.Strategy, corpusTargets(cfg), lookup, latency.New(10), models.Catalog{})

			if validErr == nil && buildErr != nil {
				t.Fatalf("ValidateConfig accepted this config but buildStrategy rejected it with %v — "+
					"every request against it would be a 500 at routing time", buildErr)
			}
		})
	}
}

// TestStrategyCorpusExercisesBothOutcomes keeps the guard above honest: a corpus
// in which nothing is ever rejected would satisfy the implication vacuously.
func TestStrategyCorpusExercisesBothOutcomes(t *testing.T) {
	var accepted, rejected int
	for _, tc := range strategyCorpus() {
		cfg := tc.cfg
		cfg.Normalize()
		if config.ValidateConfig(cfg) == nil {
			accepted++
		} else {
			rejected++
		}
	}
	if accepted == 0 || rejected == 0 {
		t.Fatalf("corpus must contain both accepted and rejected configs, got %d accepted / %d rejected", accepted, rejected)
	}
}

// TestBuildStrategy_RejectsUnknownModeIndependently: the default arm must stay,
// even though ValidateConfig already rejects an unknown mode. buildStrategy is
// reachable from a Gateway an embedder constructed without validation, and a
// silent nil strategy there is worse than an error.
func TestBuildStrategy_RejectsUnknownMode(t *testing.T) {
	lookup := func(string) (providers.Provider, bool) { return nil, false }
	_, err := buildStrategy(
		config.StrategyConfig{Mode: "roundrobin"},
		[]strategies.Target{{VirtualKey: "openai"}},
		lookup, latency.New(10), models.Catalog{},
	)
	if err == nil || !strings.Contains(err.Error(), "unknown strategy mode") {
		t.Fatalf("buildStrategy = %v, want an unknown-mode error", err)
	}
}
