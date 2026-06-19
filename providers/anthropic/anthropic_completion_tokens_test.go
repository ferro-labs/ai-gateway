package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// TestComplete_HonorsMaxCompletionTokens verifies #141: a caller supplying only
// max_completion_tokens (as the gateway seam normalizes it) is honored by the
// Anthropic provider instead of falling back to the hard-coded 1024 default.
func TestComplete_HonorsMaxCompletionTokens(t *testing.T) {
	maxCompletion := 4096
	req := core.Request{
		Model:               "claude-3-5-sonnet",
		Messages:            []core.Message{{Role: core.RoleUser, Content: "hi"}},
		MaxCompletionTokens: &maxCompletion,
	}
	// Reproduce the gateway seam (Gateway.Route calls this before dispatch).
	req.NormalizeCompletionTokenLimits()

	body := captureBody(t, req)
	if got := string(body["max_tokens"]); got != "4096" {
		t.Errorf("max_tokens = %s, want 4096 (not the 1024 default)", got)
	}
	var mt int
	if err := json.Unmarshal(body["max_tokens"], &mt); err != nil || mt == 1024 {
		t.Errorf("max_tokens decoded = %d (err %v), must not be the 1024 default", mt, err)
	}
}
