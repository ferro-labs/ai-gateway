package strategies

import (
	"context"
	"fmt"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

func TestLoadBalance_Execute(t *testing.T) {
	ma := &mockProvider{name: "a", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "a"}}
	mb := &mockProvider{name: "b", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "b"}}

	lb := NewLoadBalance(
		[]Target{{VirtualKey: "a", Weight: 50}, {VirtualKey: "b", Weight: 50}},
		newLookup(ma, mb),
	)

	// Run many times; both providers should be called at least once.
	for i := 0; i < 100; i++ {
		_, err := lb.Execute(context.Background(), providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	if ma.calls == 0 {
		t.Error("provider a was never called")
	}
	if mb.calls == 0 {
		t.Error("provider b was never called")
	}
}

func TestLoadBalance_NoTargets(t *testing.T) {
	lb := NewLoadBalance(nil, newLookup())
	_, err := lb.Execute(context.Background(), providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadBalance_FiltersUnsupportedModels(t *testing.T) {
	// "a" supports gpt-4o, "b" does not. Only "a" should receive traffic.
	ma := &mockProvider{name: "a", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "a"}}
	mb := &mockProvider{name: "b", models: []string{"claude-3"}, resp: &providers.Response{ID: "b"}}

	lb := NewLoadBalance(
		[]Target{{VirtualKey: "a", Weight: 50}, {VirtualKey: "b", Weight: 50}},
		newLookup(ma, mb),
	)

	for i := 0; i < 50; i++ {
		resp, err := lb.Execute(context.Background(), providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
		if err != nil {
			t.Fatal(err)
		}
		if resp.ID != "a" {
			t.Fatalf("request should have gone to provider a, got %s", resp.ID)
		}
	}
	if mb.calls != 0 {
		t.Errorf("provider b should not have been called, got %d calls", mb.calls)
	}
}

func TestLoadBalance_NoProviderSupportsModel(t *testing.T) {
	ma := &mockProvider{name: "a", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "a"}}

	lb := NewLoadBalance(
		[]Target{{VirtualKey: "a", Weight: 50}},
		newLookup(ma),
	)

	_, err := lb.Execute(context.Background(), providers.Request{Model: "unknown-model", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected error when no provider supports model")
	}
}

func TestLoadBalance_RespectsWeights(t *testing.T) {
	// Give "a" 90% weight and "b" 10%. Over many runs, "a" should get far more calls.
	ma := &mockProvider{name: "a", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "a"}}
	mb := &mockProvider{name: "b", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "b"}}

	lb := NewLoadBalance(
		[]Target{{VirtualKey: "a", Weight: 90}, {VirtualKey: "b", Weight: 10}},
		newLookup(ma, mb),
	)

	for i := 0; i < 1000; i++ {
		_, err := lb.Execute(context.Background(), providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
		if err != nil {
			t.Fatal(err)
		}
	}

	// With 90/10 split over 1000 requests, "a" should get at least 700.
	if ma.calls < 700 {
		t.Errorf("expected provider a to get ~900 calls, got %d", ma.calls)
	}
	if mb.calls == 0 {
		t.Error("provider b should get some calls")
	}
}

func TestLoadBalance_ZeroWeightsTreatedAsEqual(t *testing.T) {
	ma := &mockProvider{name: "a", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "a"}}
	mb := &mockProvider{name: "b", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "b"}}

	lb := NewLoadBalance(
		[]Target{{VirtualKey: "a", Weight: 0}, {VirtualKey: "b", Weight: 0}},
		newLookup(ma, mb),
	)

	for i := 0; i < 100; i++ {
		_, err := lb.Execute(context.Background(), providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	if ma.calls == 0 {
		t.Error("provider a was never called")
	}
	if mb.calls == 0 {
		t.Error("provider b was never called")
	}
}

func TestLoadBalance_ProviderError(t *testing.T) {
	ma := &mockProvider{name: "a", models: []string{"gpt-4o"}, err: fmt.Errorf("api error")}

	lb := NewLoadBalance(
		[]Target{{VirtualKey: "a", Weight: 100}},
		newLookup(ma),
	)

	_, err := lb.Execute(context.Background(), providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadBalance_MissingProvider(t *testing.T) {
	// Target references a provider that isn't registered.
	lb := NewLoadBalance(
		[]Target{{VirtualKey: "missing", Weight: 100}},
		newLookup(),
	)

	_, err := lb.Execute(context.Background(), providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected error when provider is not registered")
	}
}

func TestLoadBalance_UnresolvableSelectedTargetReturnsError(t *testing.T) {
	mp := &mockProvider{name: "a", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "a"}}
	lb := NewLoadBalance(
		[]Target{{VirtualKey: "a", Weight: 100}},
		lookupMissingAfterFirstHit(mp),
	)

	_, err := lb.Execute(context.Background(), providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected error when selected provider is no longer resolvable")
	}
}
