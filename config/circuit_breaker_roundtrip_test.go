package config_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
	"go.yaml.in/yaml/v3"
)

// A breaker block that omits a field must still omit it after a marshal:
// SQLConfigStore.Save is json.Marshal(cfg), so an omitted success_threshold
// persisted as "success_threshold":0, and the presence-tracking decoder then
// read that back as a written zero — rejecting on restart, rollback or a
// GET→PUT round trip a config it had accepted an hour earlier.
func TestCircuitBreakerConfig_RoundTripKeepsOmittedFieldsOmitted(t *testing.T) {
	const in = `{"strategy":{"mode":"single"},"targets":[{"virtual_key":"openai","circuit_breaker":{"failure_threshold":3}}]}`

	var cfg config.Config
	if err := json.Unmarshal([]byte(in), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := config.ValidateConfig(cfg); err != nil {
		t.Fatalf("first validate: %v", err)
	}

	out, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, zeroed := range []string{`"success_threshold":0`, `"max_half_threshold":0`, `"timeout":""`} {
		if strings.Contains(string(out), zeroed) {
			t.Fatalf("marshal wrote an omitted field as a zero: %s in %s", zeroed, out)
		}
	}

	var again config.Config
	if err := json.Unmarshal(out, &again); err != nil {
		t.Fatalf("unmarshal round trip: %v", err)
	}
	if err := config.ValidateConfig(again); err != nil {
		t.Fatalf("a config accepted before persistence was refused after it: %v", err)
	}
	if got := again.Targets[0].CircuitBreaker.FailureThreshold; got != 3 {
		t.Fatalf("failure_threshold = %d, want 3", got)
	}
}

func TestCircuitBreakerConfig_YAMLRoundTripKeepsOmittedFieldsOmitted(t *testing.T) {
	const in = "strategy: {mode: single}\ntargets:\n  - virtual_key: openai\n    circuit_breaker: {timeout: 45s}\n"

	var cfg config.Config
	if err := yaml.Unmarshal([]byte(in), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, zeroed := range []string{"failure_threshold: 0", "success_threshold: 0", "max_half_threshold: 0"} {
		if strings.Contains(string(out), zeroed) {
			t.Fatalf("marshal wrote an omitted field as a zero: %s in\n%s", zeroed, out)
		}
	}
	var again config.Config
	if err := yaml.Unmarshal(out, &again); err != nil {
		t.Fatalf("unmarshal round trip: %v", err)
	}
	if err := config.ValidateConfig(again); err != nil {
		t.Fatalf("a config accepted before persistence was refused after it: %v", err)
	}
}
