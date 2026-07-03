package anthropicwire

import (
	"context"

	"github.com/ferro-labs/ai-gateway/internal/logging"
)

// maxAnthropicTemperature is the upper bound the Anthropic Messages API accepts
// for the temperature sampling parameter. The gateway's OpenAI-compatible seam
// validates the wider 0–2 range, so a caller-supplied value can exceed it.
const maxAnthropicTemperature = 1.0

// ClampTemperature constrains an OpenAI-range temperature (0–2) to the range the
// Anthropic Messages API accepts (0–1). A value above 1 is clamped to 1 and a
// warn-level log records the adjustment so it is observable rather than a silent
// upstream 400; in-range and nil values pass through unchanged. It never mutates
// the caller's value — a clamp returns a fresh pointer.
func ClampTemperature(ctx context.Context, provider, model string, temp *float64) *float64 {
	if temp == nil || *temp <= maxAnthropicTemperature {
		return temp
	}
	logging.FromContext(ctx).Warn(
		"temperature above provider maximum; clamping",
		"provider", provider,
		"model", model,
		"requested", *temp,
		"clamped_to", maxAnthropicTemperature,
	)
	clamped := maxAnthropicTemperature
	return &clamped
}
