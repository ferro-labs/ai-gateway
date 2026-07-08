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

// TestUsage_UnmarshalDeepSeekPromptCacheHitTokens verifies DeepSeek's flat
// (non-nested) prompt_cache_hit_tokens field folds into CacheReadTokens, same
// as OpenAI's nested prompt_tokens_details.cached_tokens. This is what makes
// DeepSeek's streaming path (which decodes usage straight into core.Usage via
// this method) surface cache-hit tokens; the non-streaming path already
// captures it through DeepSeek's own hand-rolled usage struct.
func TestUsage_UnmarshalDeepSeekPromptCacheHitTokens(t *testing.T) {
	var u Usage
	if err := json.Unmarshal([]byte(`{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_cache_hit_tokens":6,"prompt_cache_miss_tokens":4}`), &u); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if u.CacheReadTokens != 6 {
		t.Errorf("CacheReadTokens = %d, want 6 (from prompt_cache_hit_tokens)", u.CacheReadTokens)
	}
}

// TestUsage_FlatCacheReadTokensBeatsPromptCacheHitTokens verifies the
// existing flat field still wins over DeepSeek's field, same precedence rule
// as the nested OpenAI detail.
func TestUsage_FlatCacheReadTokensBeatsPromptCacheHitTokens(t *testing.T) {
	var u Usage
	if err := json.Unmarshal([]byte(`{"cache_read_tokens":9,"prompt_cache_hit_tokens":6}`), &u); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if u.CacheReadTokens != 9 {
		t.Errorf("CacheReadTokens = %d, want 9 (flat precedence)", u.CacheReadTokens)
	}
}
