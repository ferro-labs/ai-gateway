package strategies

import (
	"context"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

func req(content string) providers.Request {
	return providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: content}},
	}
}

func TestContentBased_PromptContains(t *testing.T) {
	provA := &mockProvider{name: "code-model", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "code"}}
	provB := &mockProvider{name: "general-model", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "general"}}
	lookup := newLookup(provA, provB)

	rules := []ContentRule{
		{Type: PromptContains, Value: "python", Target: Target{VirtualKey: "code-model"}},
	}
	s, err := NewContentBased(rules, Target{VirtualKey: "general-model"}, lookup)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := s.Execute(context.Background(), req("write a python function"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "code" {
		t.Errorf("expected code-model, got %q", resp.ID)
	}
}

func TestContentBased_FallbackWhenNoMatch(t *testing.T) {
	provA := &mockProvider{name: "code-model", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "code"}}
	provB := &mockProvider{name: "general-model", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "general"}}
	lookup := newLookup(provA, provB)

	rules := []ContentRule{
		{Type: PromptContains, Value: "python", Target: Target{VirtualKey: "code-model"}},
	}
	s, err := NewContentBased(rules, Target{VirtualKey: "general-model"}, lookup)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := s.Execute(context.Background(), req("what is the weather today?"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "general" {
		t.Errorf("expected general-model (fallback), got %q", resp.ID)
	}
}

func TestContentBased_PromptCaseInsensitive(t *testing.T) {
	provA := &mockProvider{name: "code-model", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "code"}}
	provB := &mockProvider{name: "general-model", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "general"}}
	lookup := newLookup(provA, provB)

	rules := []ContentRule{
		{Type: PromptContains, Value: "Python", Target: Target{VirtualKey: "code-model"}},
	}
	s, err := NewContentBased(rules, Target{VirtualKey: "general-model"}, lookup)
	if err != nil {
		t.Fatal(err)
	}

	// "PYTHON" should match "Python" rule (case-insensitive)
	resp, err := s.Execute(context.Background(), req("help me with PYTHON scripting"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "code" {
		t.Errorf("expected code-model (case-insensitive match), got %q", resp.ID)
	}
}

func TestContentBased_PromptNotContains(t *testing.T) {
	provA := &mockProvider{name: "cheap-model", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "cheap"}}
	provB := &mockProvider{name: "smart-model", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "smart"}}
	lookup := newLookup(provA, provB)

	// Route everything that is NOT code-related to cheap-model.
	rules := []ContentRule{
		{Type: PromptNotContains, Value: "code", Target: Target{VirtualKey: "cheap-model"}},
	}
	s, err := NewContentBased(rules, Target{VirtualKey: "smart-model"}, lookup)
	if err != nil {
		t.Fatal(err)
	}

	// No "code" → matches PromptNotContains → cheap-model
	resp, err := s.Execute(context.Background(), req("tell me a joke"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "cheap" {
		t.Errorf("expected cheap-model, got %q", resp.ID)
	}

	// Contains "code" → PromptNotContains is false → no match → fallback smart-model
	resp, err = s.Execute(context.Background(), req("review my code"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "smart" {
		t.Errorf("expected smart-model (fallback), got %q", resp.ID)
	}
}

func TestContentBased_PromptRegex(t *testing.T) {
	provA := &mockProvider{name: "code-model", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "code"}}
	provB := &mockProvider{name: "general-model", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "general"}}
	lookup := newLookup(provA, provB)

	rules := []ContentRule{
		{Type: PromptRegex, Value: `(?i)(python|golang|typescript)`, Target: Target{VirtualKey: "code-model"}},
	}
	s, err := NewContentBased(rules, Target{VirtualKey: "general-model"}, lookup)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		prompt   string
		wantResp string
	}{
		{"How do I write a Golang HTTP server?", "code"},
		{"Explain TypeScript generics", "code"},
		{"What is the capital of France?", "general"},
	}
	for _, tt := range tests {
		t.Run(tt.prompt, func(t *testing.T) {
			resp, err := s.Execute(context.Background(), req(tt.prompt))
			if err != nil {
				t.Fatal(err)
			}
			if resp.ID != tt.wantResp {
				t.Errorf("prompt %q: got %q, want %q", tt.prompt, resp.ID, tt.wantResp)
			}
		})
	}
}

func TestContentBased_InvalidRegex(t *testing.T) {
	lookup := newLookup()
	rules := []ContentRule{
		{Type: PromptRegex, Value: `[invalid`, Target: Target{VirtualKey: "any"}},
	}
	_, err := NewContentBased(rules, Target{VirtualKey: "any"}, lookup)
	if err == nil {
		t.Fatal("expected error for invalid regex pattern")
	}
}

func TestContentBased_FirstRuleWins(t *testing.T) {
	provA := &mockProvider{name: "model-a", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "a"}}
	provB := &mockProvider{name: "model-b", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "b"}}
	lookup := newLookup(provA, provB)

	// Both rules match — first rule should win.
	rules := []ContentRule{
		{Type: PromptContains, Value: "python", Target: Target{VirtualKey: "model-a"}},
		{Type: PromptContains, Value: "code", Target: Target{VirtualKey: "model-b"}},
	}
	s, err := NewContentBased(rules, Target{VirtualKey: "model-a"}, lookup)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := s.Execute(context.Background(), req("write python code"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "a" {
		t.Errorf("first matching rule should win, got %q", resp.ID)
	}
}

func TestContentBased_OnlyUserRoleMessagesChecked(t *testing.T) {
	provA := &mockProvider{name: "code-model", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "code"}}
	provB := &mockProvider{name: "general-model", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "general"}}
	lookup := newLookup(provA, provB)

	rules := []ContentRule{
		{Type: PromptContains, Value: "python", Target: Target{VirtualKey: "code-model"}},
	}
	s, err := NewContentBased(rules, Target{VirtualKey: "general-model"}, lookup)
	if err != nil {
		t.Fatal(err)
	}

	// "python" only appears in a system message, not a user message → no match → fallback
	sysReq := providers.Request{
		Model: "gpt-4o",
		Messages: []providers.Message{
			{Role: "system", Content: "You are a python expert"},
			{Role: "user", Content: "explain recursion"},
		},
	}
	resp, err := s.Execute(context.Background(), sysReq)
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "general" {
		t.Errorf("only user messages should be checked, got %q instead of fallback", resp.ID)
	}
}

func TestContentBased_ProviderNotFound(t *testing.T) {
	s, err := NewContentBased(nil, Target{VirtualKey: "missing"}, newLookup())
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Execute(context.Background(), req("hello"))
	if err == nil {
		t.Fatal("expected error when provider not found")
	}
}
