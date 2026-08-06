package core

import (
	"encoding/json"
	"testing"
)

// The OpenAI usage object reports cached prompt tokens and reasoning completion
// tokens inside two nested objects. The gateway folds both into flat fields so
// cost accounting can read them without knowing which wire shape a provider
// used — and used to stop there, so a client reading the names the OpenAI
// schema defines found nothing at all.
func TestUsageMarshalsOpenAITokenDetails(t *testing.T) {
	u := Usage{
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
		CacheReadTokens:  64,
		ReasoningTokens:  33,
	}

	b, err := json.Marshal(u)
	if err != nil {
		t.Fatalf("Marshal(): %v", err)
	}

	var got struct {
		PromptTokensDetails     *PromptTokensDetails     `json:"prompt_tokens_details"`
		CompletionTokensDetails *CompletionTokensDetails `json:"completion_tokens_details"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal(): %v", err)
	}
	if got.PromptTokensDetails == nil {
		t.Fatalf("prompt_tokens_details absent from %s", b)
	}
	if got.PromptTokensDetails.CachedTokens != 64 {
		t.Errorf("prompt_tokens_details.cached_tokens = %d, want 64", got.PromptTokensDetails.CachedTokens)
	}
	if got.CompletionTokensDetails == nil {
		t.Fatalf("completion_tokens_details absent from %s", b)
	}
	if got.CompletionTokensDetails.ReasoningTokens != 33 {
		t.Errorf("completion_tokens_details.reasoning_tokens = %d, want 33", got.CompletionTokensDetails.ReasoningTokens)
	}
}

// The flat counters stay on the wire beside the nested objects. cache_write has
// no OpenAI equivalent at all, so dropping the flat set would lose it outright,
// and every client already reading the other two would break.
func TestUsageKeepsFlatCountersAlongsideDetails(t *testing.T) {
	b, err := json.Marshal(Usage{CacheReadTokens: 8, ReasoningTokens: 4, CacheWriteTokens: 2})
	if err != nil {
		t.Fatalf("Marshal(): %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal(): %v", err)
	}
	for _, key := range []string{"reasoning_tokens", "cache_read_tokens", "cache_write_tokens"} {
		if _, ok := got[key]; !ok {
			t.Errorf("%s absent from %s", key, b)
		}
	}
}

// A provider that reports neither must not gain two empty objects: the details
// are omitted, not zeroed, so "not reported" stays distinguishable from "zero".
func TestUsageOmitsTokenDetailsWhenUnreported(t *testing.T) {
	b, err := json.Marshal(Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5})
	if err != nil {
		t.Fatalf("Marshal(): %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal(): %v", err)
	}
	for _, key := range []string{"prompt_tokens_details", "completion_tokens_details"} {
		if _, ok := got[key]; ok {
			t.Errorf("%s present for a provider that reported no breakdown: %s", key, b)
		}
	}
}

// Encoding and decoding are one contract: what the gateway writes it must read
// back unchanged, or a cached or replayed response loses its breakdown.
func TestUsageRoundTripsTokenDetails(t *testing.T) {
	want := Usage{
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
		ReasoningTokens:  33,
		CacheReadTokens:  64,
		CacheWriteTokens: 12,
	}

	b, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal(): %v", err)
	}
	var got Usage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal(): %v", err)
	}
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

// Ollama's OpenAI-compatible endpoint names the field "reasoning". Reading only
// reasoning_content discarded the whole reasoning output of every model served
// through it, with nothing to show the caller it had been dropped.
func TestMessageDecodesProviderReasoningField(t *testing.T) {
	var msg Message
	if err := json.Unmarshal([]byte(`{"role":"assistant","content":"4","reasoning":"2+2 is 4"}`), &msg); err != nil {
		t.Fatalf("Unmarshal(): %v", err)
	}
	if msg.ReasoningContent != "2+2 is 4" {
		t.Errorf("ReasoningContent = %q, want %q", msg.ReasoningContent, "2+2 is 4")
	}
}

// The canonical name wins when a provider sends both, so adding the alias
// cannot change what an already-correct provider produces.
func TestMessagePrefersReasoningContentOverAlias(t *testing.T) {
	var msg Message
	body := `{"role":"assistant","content":"4","reasoning":"alias","reasoning_content":"canonical"}`
	if err := json.Unmarshal([]byte(body), &msg); err != nil {
		t.Fatalf("Unmarshal(): %v", err)
	}
	if msg.ReasoningContent != "canonical" {
		t.Errorf("ReasoningContent = %q, want %q", msg.ReasoningContent, "canonical")
	}
}

// The streaming half of the same gap: the delta carries the alias too.
func TestMessageDeltaDecodesProviderReasoningField(t *testing.T) {
	var delta MessageDelta
	if err := json.Unmarshal([]byte(`{"role":"assistant","reasoning":"thinking"}`), &delta); err != nil {
		t.Fatalf("Unmarshal(): %v", err)
	}
	if delta.ReasoningContent != "thinking" {
		t.Errorf("ReasoningContent = %q, want %q", delta.ReasoningContent, "thinking")
	}
	if delta.Role != RoleAssistant {
		t.Errorf("Role = %q, want %q; the alias decode must not drop the other fields", delta.Role, RoleAssistant)
	}
}
