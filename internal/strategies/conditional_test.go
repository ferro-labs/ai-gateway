package strategies

import (
	"context"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

func TestConditional_MatchesModel(t *testing.T) {
	openai := &mockProvider{name: "openai", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "openai-resp"}}
	anthropic := &mockProvider{name: "anthropic", models: []string{"claude-3"}, resp: &providers.Response{ID: "anthropic-resp"}}

	rules := []ConditionRule{
		{Key: "model", Value: "gpt-4o", Target: Target{VirtualKey: "openai"}},
		{Key: "model", Value: "claude-3", Target: Target{VirtualKey: "anthropic"}},
	}
	c := NewConditional(rules, Target{VirtualKey: "openai"}, newLookup(openai, anthropic))

	resp, err := c.Execute(context.Background(), providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "openai-resp" {
		t.Errorf("expected openai-resp, got %s", resp.ID)
	}

	resp, err = c.Execute(context.Background(), providers.Request{Model: "claude-3", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "anthropic-resp" {
		t.Errorf("expected anthropic-resp, got %s", resp.ID)
	}
}

func TestConditional_ModelPrefix(t *testing.T) {
	openai := &mockProvider{name: "openai", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "openai-resp"}}
	anthropic := &mockProvider{name: "anthropic", models: []string{"claude-3-opus"}, resp: &providers.Response{ID: "anthropic-resp"}}

	rules := []ConditionRule{
		{Key: "model_prefix", Value: "gpt-", Target: Target{VirtualKey: "openai"}},
		{Key: "model_prefix", Value: "claude-", Target: Target{VirtualKey: "anthropic"}},
	}
	c := NewConditional(rules, Target{VirtualKey: "openai"}, newLookup(openai, anthropic))

	resp, err := c.Execute(context.Background(), providers.Request{Model: "claude-3-opus", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "anthropic-resp" {
		t.Errorf("expected anthropic-resp, got %s", resp.ID)
	}
}

func TestConditional_Fallback(t *testing.T) {
	fallbackProvider := &mockProvider{name: "fallback", models: []string{"any"}, resp: &providers.Response{ID: "fallback-resp"}}

	rules := []ConditionRule{
		{Key: "model", Value: "gpt-4o", Target: Target{VirtualKey: "nonexistent"}},
	}
	c := NewConditional(rules, Target{VirtualKey: "fallback"}, newLookup(fallbackProvider))

	resp, err := c.Execute(context.Background(), providers.Request{Model: "unknown-model", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "fallback-resp" {
		t.Errorf("expected fallback-resp, got %s", resp.ID)
	}
}

func TestConditional_ProviderNotFound(t *testing.T) {
	rules := []ConditionRule{
		{Key: "model", Value: "gpt-4o", Target: Target{VirtualKey: "missing"}},
	}
	c := NewConditional(rules, Target{VirtualKey: "also-missing"}, newLookup())

	_, err := c.Execute(context.Background(), providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected error for missing provider")
	}
}

func TestConditional_UnknownKeyNeverMatches(t *testing.T) {
	fb := &mockProvider{name: "fb", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "fb"}}
	other := &mockProvider{name: "other", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "other"}}

	rules := []ConditionRule{
		{Key: "unknown_key", Value: "gpt-4o", Target: Target{VirtualKey: "other"}},
	}
	c := NewConditional(rules, Target{VirtualKey: "fb"}, newLookup(fb, other))

	resp, err := c.Execute(context.Background(), providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "fb" {
		t.Errorf("expected fallback, got %s", resp.ID)
	}
	if other.calls != 0 {
		t.Error("other provider should not have been called")
	}
}

func TestConditional_FirstRuleWins(t *testing.T) {
	p1 := &mockProvider{name: "p1", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "p1"}}
	p2 := &mockProvider{name: "p2", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "p2"}}

	// Both rules match "gpt-4o" (exact and prefix). First should win.
	rules := []ConditionRule{
		{Key: "model", Value: "gpt-4o", Target: Target{VirtualKey: "p1"}},
		{Key: "model_prefix", Value: "gpt-", Target: Target{VirtualKey: "p2"}},
	}
	c := NewConditional(rules, Target{VirtualKey: "p2"}, newLookup(p1, p2))

	resp, err := c.Execute(context.Background(), providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "p1" {
		t.Errorf("expected first rule match (p1), got %s", resp.ID)
	}
	if p2.calls != 0 {
		t.Error("p2 should not have been called")
	}
}

func TestConditional_NoRulesUsesFallback(t *testing.T) {
	fb := &mockProvider{name: "fb", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "fb"}}

	c := NewConditional(nil, Target{VirtualKey: "fb"}, newLookup(fb))

	resp, err := c.Execute(context.Background(), providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "fb" {
		t.Errorf("expected fallback, got %s", resp.ID)
	}
}
