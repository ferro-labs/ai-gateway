package core

import (
	"encoding/json"
	"testing"
)

// TestUsage_UnmarshalNestedDetails verifies the nested OpenAI usage details fold
// into the flat extended fields.
func TestUsage_UnmarshalNestedDetails(t *testing.T) {
	var u Usage
	if err := json.Unmarshal([]byte(`{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":7}}`), &u); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if u.CacheReadTokens != 4 {
		t.Errorf("CacheReadTokens = %d, want 4 (from prompt_tokens_details)", u.CacheReadTokens)
	}
	if u.ReasoningTokens != 7 {
		t.Errorf("ReasoningTokens = %d, want 7 (from completion_tokens_details)", u.ReasoningTokens)
	}
	if u.PromptTokens != 10 || u.CompletionTokens != 20 || u.TotalTokens != 30 {
		t.Errorf("flat fields not preserved: %+v", u)
	}
}

// TestUsage_FlatFieldTakesPrecedence verifies an explicit flat field wins over
// the nested detail when both are present.
func TestUsage_FlatFieldTakesPrecedence(t *testing.T) {
	var u Usage
	if err := json.Unmarshal([]byte(`{"cache_read_tokens":9,"prompt_tokens_details":{"cached_tokens":4}}`), &u); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if u.CacheReadTokens != 9 {
		t.Errorf("CacheReadTokens = %d, want 9 (flat precedence)", u.CacheReadTokens)
	}
}
