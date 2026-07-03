package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// TestComplete_ClampsTemperatureToAnthropicRange verifies that a temperature in
// the gateway's OpenAI range (0–2) but above Anthropic's maximum (1) is clamped
// to 1 on the outgoing request rather than forwarded to a 400.
func TestComplete_ClampsTemperatureToAnthropicRange(t *testing.T) {
	body := captureBody(t, core.Request{
		Model:       "claude-3-5-sonnet",
		Messages:    []core.Message{{Role: core.RoleUser, Content: "hi"}},
		Temperature: floatPtr(1.8),
	})

	raw, ok := body["temperature"]
	if !ok {
		t.Fatal("temperature not forwarded")
	}
	var temp float64
	if err := json.Unmarshal(raw, &temp); err != nil {
		t.Fatalf("decode temperature: %v", err)
	}
	if temp != 1.0 {
		t.Fatalf("temperature = %v, want clamped to 1.0", temp)
	}
}

// TestComplete_ForwardsInRangeTemperature confirms an in-range value passes
// through untouched.
func TestComplete_ForwardsInRangeTemperature(t *testing.T) {
	body := captureBody(t, core.Request{
		Model:       "claude-3-5-sonnet",
		Messages:    []core.Message{{Role: core.RoleUser, Content: "hi"}},
		Temperature: floatPtr(0.6),
	})
	var temp float64
	if err := json.Unmarshal(body["temperature"], &temp); err != nil {
		t.Fatalf("decode temperature: %v", err)
	}
	if temp != 0.6 {
		t.Fatalf("temperature = %v, want 0.6", temp)
	}
}
