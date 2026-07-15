package strategies

import (
	"context"
	"fmt"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

func TestSingle_Execute(t *testing.T) {
	mp := &mockProvider{name: "a", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "ok"}}
	s := NewSingle(Target{VirtualKey: "a"}, newLookup(mp))

	resp, err := s.Execute(context.Background(), providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "ok" {
		t.Errorf("got %q, want ok", resp.ID)
	}
	if resp.Provider != "a" {
		t.Errorf("got provider %q, want a", resp.Provider)
	}
}

func TestSingle_ExecutePreservesProvider(t *testing.T) {
	mp := &mockProvider{
		name:   "a",
		models: []string{"gpt-4o"},
		resp:   &providers.Response{ID: "ok", Provider: "upstream-name"},
	}
	s := NewSingle(Target{VirtualKey: "a"}, newLookup(mp))

	resp, err := s.Execute(context.Background(), providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "upstream-name" {
		t.Errorf("got provider %q, want upstream-name", resp.Provider)
	}
}

func TestSingle_ProviderNotFound(t *testing.T) {
	s := NewSingle(Target{VirtualKey: "missing"}, newLookup())
	_, err := s.Execute(context.Background(), providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestSingle_UnsupportedModel(t *testing.T) {
	mp := &mockProvider{name: "a", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "ok"}}
	s := NewSingle(Target{VirtualKey: "a"}, newLookup(mp))

	_, err := s.Execute(context.Background(), providers.Request{Model: "claude-3", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected error for unsupported model")
	}
	if mp.calls != 0 {
		t.Error("provider should not have been called")
	}
}

func TestSingle_ProviderError(t *testing.T) {
	mp := &mockProvider{name: "a", models: []string{"gpt-4o"}, err: fmt.Errorf("api down")}
	s := NewSingle(Target{VirtualKey: "a"}, newLookup(mp))

	_, err := s.Execute(context.Background(), providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected error")
	}
	if mp.calls != 1 {
		t.Errorf("expected 1 call, got %d", mp.calls)
	}
}
