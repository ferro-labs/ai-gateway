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

// TestSingle_ExecuteAlwaysStampsRoutingAlias asserts the routing alias always
// wins over whatever the provider itself reported in Response.Provider. Every
// real provider hardcodes its own canonical name into Response.Provider
// before the strategy layer ever sees it, so if the strategy preserved a
// non-empty upstream value, the routing alias used to look up a multi-instance
// target (e.g. two same-type provider instances registered under distinct
// aliases) would never surface in resp.Provider — defeating the purpose of
// per-instance metrics/logs/traces.
func TestSingle_ExecuteAlwaysStampsRoutingAlias(t *testing.T) {
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
	if resp.Provider != "a" {
		t.Errorf("got provider %q, want routing alias %q", resp.Provider, "a")
	}
}

// lookupByAlias builds a ProviderLookup keyed by the supplied alias rather
// than by each provider's own Name() — mirroring how the gateway registers
// provider_instances: two independently-credentialed instances of the same
// underlying provider type, each addressed by a distinct routing alias, with
// both providers reporting the same canonical Name() (they're the same
// underlying type).
func lookupByAlias(pp map[string]providers.Provider) ProviderLookup {
	return func(alias string) (providers.Provider, bool) {
		p, ok := pp[alias]
		return p, ok
	}
}

// TestSingle_Execute_MultiInstanceSameCanonicalName is the regression test for
// the multi-instance bug: two provider instances share one canonical Name()
// (e.g. "ollama-cloud") but are registered under distinct routing aliases
// ("ollama-cloud-a", "ollama-cloud-b"). Routing to each alias must stamp that
// alias onto resp.Provider — never the shared canonical name both providers
// happen to report — otherwise metrics/logs/traces could never tell the two
// instances apart.
func TestSingle_Execute_MultiInstanceSameCanonicalName(t *testing.T) {
	instanceA := &mockProvider{
		name:   "ollama-cloud",
		models: []string{"model-a"},
		resp:   &providers.Response{ID: "resp-a", Provider: "ollama-cloud", Model: "model-a"},
	}
	instanceB := &mockProvider{
		name:   "ollama-cloud",
		models: []string{"model-b"},
		resp:   &providers.Response{ID: "resp-b", Provider: "ollama-cloud", Model: "model-b"},
	}
	lookup := lookupByAlias(map[string]providers.Provider{
		"ollama-cloud-a": instanceA,
		"ollama-cloud-b": instanceB,
	})

	sa := NewSingle(Target{VirtualKey: "ollama-cloud-a"}, lookup)
	respA, err := sa.Execute(context.Background(), providers.Request{Model: "model-a", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if respA.Provider != "ollama-cloud-a" {
		t.Errorf("instance A: got provider %q, want alias %q", respA.Provider, "ollama-cloud-a")
	}

	sb := NewSingle(Target{VirtualKey: "ollama-cloud-b"}, lookup)
	respB, err := sb.Execute(context.Background(), providers.Request{Model: "model-b", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if respB.Provider != "ollama-cloud-b" {
		t.Errorf("instance B: got provider %q, want alias %q", respB.Provider, "ollama-cloud-b")
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
