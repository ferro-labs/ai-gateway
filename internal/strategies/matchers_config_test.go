package strategies

import (
	"slices"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
)

// The matcher switches here and the accepted-value lists in config are two
// halves of one rule: config decides which values load, the switches decide what
// they do. Drift either way is silent and costs a whole rule's traffic — a value
// config accepts and no switch arm matches routes to the fallback instead (F16),
// and a value a switch matches but config rejects is a config nobody can write.
// Both constant sets below are the switch cases themselves, so comparing them to
// config's lists is the whole check.
//
// Importing config from a test rather than from the package keeps that guard
// without making internal/strategies depend on the config schema — or config
// depend, transitively, on every provider package.

func TestConditionKeysMatchConfig(t *testing.T) {
	assertSameSet(t, "condition key",
		config.ConditionKeys(),
		[]string{ConditionKeyModel, ConditionKeyModelPrefix})
}

func TestContentConditionTypesMatchConfig(t *testing.T) {
	assertSameSet(t, "content condition type",
		config.ContentConditionTypes(),
		[]string{string(PromptContains), string(PromptNotContains), string(PromptRegex)})
}

func assertSameSet(t *testing.T, kind string, fromConfig, fromStrategies []string) {
	t.Helper()
	for _, v := range fromConfig {
		if !slices.Contains(fromStrategies, v) {
			t.Errorf("config accepts %s %q, but no matcher arm handles it — that rule's traffic would silently go to the fallback", kind, v)
		}
	}
	for _, v := range fromStrategies {
		if !slices.Contains(fromConfig, v) {
			t.Errorf("a matcher arm handles %s %q, but config.ValidateConfig rejects it — nobody can write that config", kind, v)
		}
	}
}
