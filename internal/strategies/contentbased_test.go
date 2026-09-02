package strategies

import (
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

func req(content string) providers.Request {
	return providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: content}},
	}
}

// newContentBased builds a ContentBased or fails the test — every case here
// uses patterns that compile.
func newContentBased(t *testing.T, rules []ContentRule, fallback string) *ContentBased {
	t.Helper()
	s, err := NewContentBased(rules, Target{VirtualKey: fallback})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestContentBased_PromptContains(t *testing.T) {
	s := newContentBased(t, []ContentRule{
		{Type: PromptContains, Value: "python", Targets: []Target{{VirtualKey: "code-model"}}},
	}, "general-model")

	assertLeadsWith(t, s, req("write a python function"), "code-model")
}

func TestContentBased_FallbackWhenNoMatch(t *testing.T) {
	s := newContentBased(t, []ContentRule{
		{Type: PromptContains, Value: "python", Targets: []Target{{VirtualKey: "code-model"}}},
	}, "general-model")

	assertLeadsWith(t, s, req("what is the weather today?"), "general-model")
}

func TestContentBased_PromptCaseInsensitive(t *testing.T) {
	s := newContentBased(t, []ContentRule{
		{Type: PromptContains, Value: "Python", Targets: []Target{{VirtualKey: "code-model"}}},
	}, "general-model")

	assertLeadsWith(t, s, req("help me with PYTHON scripting"), "code-model")
}

func TestContentBased_PromptNotContains(t *testing.T) {
	// Route everything that is NOT code-related to cheap-model.
	s := newContentBased(t, []ContentRule{
		{Type: PromptNotContains, Value: "code", Targets: []Target{{VirtualKey: "cheap-model"}}},
	}, "smart-model")

	// No "code" → matches PromptNotContains → cheap-model.
	assertLeadsWith(t, s, req("tell me a joke"), "cheap-model")
	// Contains "code" → PromptNotContains is false → no match → fallback.
	assertLeadsWith(t, s, req("review my code"), "smart-model")
}

func TestContentBased_PromptRegex(t *testing.T) {
	s := newContentBased(t, []ContentRule{
		{Type: PromptRegex, Value: `(?i)(python|golang|typescript)`, Targets: []Target{{VirtualKey: "code-model"}}},
	}, "general-model")

	tests := []struct {
		prompt string
		want   string
	}{
		{"How do I write a Golang HTTP server?", "code-model"},
		{"Explain TypeScript generics", "code-model"},
		{"What is the capital of France?", "general-model"},
	}
	for _, tt := range tests {
		t.Run(tt.prompt, func(t *testing.T) {
			assertLeadsWith(t, s, req(tt.prompt), tt.want)
		})
	}
}

func TestContentBased_InvalidRegex(t *testing.T) {
	rules := []ContentRule{
		{Type: PromptRegex, Value: `[invalid`, Targets: []Target{{VirtualKey: "any"}}},
	}
	if _, err := NewContentBased(rules, Target{VirtualKey: "any"}); err == nil {
		t.Fatal("expected error for invalid regex pattern")
	}
}

func TestContentBased_FirstRuleWins(t *testing.T) {
	// Both rules match — first rule should win.
	s := newContentBased(t, []ContentRule{
		{Type: PromptContains, Value: "python", Targets: []Target{{VirtualKey: "model-a"}}},
		{Type: PromptContains, Value: "code", Targets: []Target{{VirtualKey: "model-b"}}},
	}, "model-a")

	assertLeadsWith(t, s, req("write python code"), "model-a")
}

func TestContentBased_OnlyUserRoleMessagesChecked(t *testing.T) {
	s := newContentBased(t, []ContentRule{
		{Type: PromptContains, Value: "python", Targets: []Target{{VirtualKey: "code-model"}}},
	}, "general-model")

	// "python" appears only in a system message → no match → fallback.
	sysReq := providers.Request{
		Model: "gpt-4o",
		Messages: []providers.Message{
			{Role: "system", Content: "You are a python expert"},
			{Role: "user", Content: "explain recursion"},
		},
	}
	assertLeadsWith(t, s, sysReq, "general-model")
}

// TestContentBased_UnknownTypeNeverMatches is F16's content-based half — see
// TestConditional_UnknownKeyNeverMatches.
func TestContentBased_UnknownTypeNeverMatches(t *testing.T) {
	s := newContentBased(t, []ContentRule{
		{Type: "prompt_contain", Value: "python", Targets: []Target{{VirtualKey: "code-model"}}},
	}, "general-model")

	assertLeadsWith(t, s, req("write python code"), "general-model")
}

// TestContentBased_SelectTargets_FallbackWithoutRoutingTargets asserts that a
// ContentBased built without WithRoutingTargets still surfaces its no-match
// fallback rather than an empty list.
func TestContentBased_SelectTargets_FallbackWithoutRoutingTargets(t *testing.T) {
	s := newContentBased(t, []ContentRule{
		{Type: PromptContains, Value: "python", Targets: []Target{{VirtualKey: "code-model"}}},
	}, "general-model")

	keys, err := s.SelectTargets(req("what is the weather?"))
	if err != nil {
		t.Fatal(err)
	}
	assertKeys(t, keys, "general-model")
}

// TestContentBased_SelectTargets_MatchThenFallback asserts a matched rule leads,
// with the fallback appended, even without WithRoutingTargets.
func TestContentBased_SelectTargets_MatchThenFallback(t *testing.T) {
	s := newContentBased(t, []ContentRule{
		{Type: PromptContains, Value: "python", Targets: []Target{{VirtualKey: "code-model"}}},
	}, "general-model")

	keys, err := s.SelectTargets(req("write python code"))
	if err != nil {
		t.Fatal(err)
	}
	assertKeys(t, keys, "code-model")
}
