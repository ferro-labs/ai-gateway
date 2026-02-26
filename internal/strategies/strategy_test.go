package strategies

import (
	"context"
	"fmt"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

type mockProvider struct {
	name   string
	models []string
	resp   *providers.Response
	err    error
	calls  int
}

func (m *mockProvider) Name() string              { return m.name }
func (m *mockProvider) SupportedModels() []string  { return m.models }
func (m *mockProvider) Models() []providers.ModelInfo { return nil }
func (m *mockProvider) SupportsModel(model string) bool {
	for _, mm := range m.models {
		if mm == model {
			return true
		}
	}
	return false
}
func (m *mockProvider) Complete(_ context.Context, _ providers.Request) (*providers.Response, error) {
	m.calls++
	return m.resp, m.err
}

func newLookup(pp ...providers.Provider) ProviderLookup {
	m := make(map[string]providers.Provider)
	for _, p := range pp {
		m[p.Name()] = p
	}
	return func(name string) (providers.Provider, bool) {
		p, ok := m[name]
		return p, ok
	}
}

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

func TestFallback_FirstSucceeds(t *testing.T) {
	mp := &mockProvider{name: "a", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "a-ok"}}
	f := NewFallback([]Target{{VirtualKey: "a"}}, newLookup(mp))

	resp, err := f.Execute(context.Background(), providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "a-ok" {
		t.Errorf("got %q", resp.ID)
	}
}

func TestFallback_FallsToSecond(t *testing.T) {
	bad := &mockProvider{name: "bad", models: []string{"gpt-4o"}, err: fmt.Errorf("down")}
	good := &mockProvider{name: "good", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "recovered"}}

	f := NewFallback(
		[]Target{{VirtualKey: "bad"}, {VirtualKey: "good"}},
		newLookup(bad, good),
	)

	resp, err := f.Execute(context.Background(), providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "recovered" {
		t.Errorf("got %q, want recovered", resp.ID)
	}
}

func TestFallback_AllFail(t *testing.T) {
	bad1 := &mockProvider{name: "a", models: []string{"gpt-4o"}, err: fmt.Errorf("fail1")}
	bad2 := &mockProvider{name: "b", models: []string{"gpt-4o"}, err: fmt.Errorf("fail2")}

	f := NewFallback(
		[]Target{{VirtualKey: "a"}, {VirtualKey: "b"}},
		newLookup(bad1, bad2),
	)

	_, err := f.Execute(context.Background(), providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected error when all providers fail")
	}
}

func TestFallback_NoTargets(t *testing.T) {
	f := NewFallback(nil, newLookup())
	_, err := f.Execute(context.Background(), providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected error for no targets")
	}
}

func TestFallback_SkipsUnsupportedModel(t *testing.T) {
	// First provider doesn't support the model, second does.
	wrong := &mockProvider{name: "wrong", models: []string{"claude-3"}, resp: &providers.Response{ID: "wrong"}}
	right := &mockProvider{name: "right", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "right"}}

	f := NewFallback(
		[]Target{{VirtualKey: "wrong"}, {VirtualKey: "right"}},
		newLookup(wrong, right),
	)

	resp, err := f.Execute(context.Background(), providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "right" {
		t.Errorf("expected right, got %s", resp.ID)
	}
	if wrong.calls != 0 {
		t.Error("unsupported provider should not have been called")
	}
}

func TestFallback_WithMaxRetries(t *testing.T) {
	// Provider fails on first 2 attempts, never succeeds. With 3 retries, all 3 attempts are made.
	bad := &mockProvider{name: "a", models: []string{"gpt-4o"}, err: fmt.Errorf("fail")}

	f := NewFallback(
		[]Target{{VirtualKey: "a"}},
		newLookup(bad),
	).WithMaxRetries(3)

	_, err := f.Execute(context.Background(), providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected error")
	}
	if bad.calls != 3 {
		t.Errorf("expected 3 retry attempts, got %d", bad.calls)
	}
}

func TestFallback_SkipsMissingProvider(t *testing.T) {
	good := &mockProvider{name: "good", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "good"}}

	// First target's provider is not registered, should skip to second.
	f := NewFallback(
		[]Target{{VirtualKey: "missing"}, {VirtualKey: "good"}},
		newLookup(good),
	)

	resp, err := f.Execute(context.Background(), providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "good" {
		t.Errorf("expected good, got %s", resp.ID)
	}
}

func TestFallback_ContextCancelled(t *testing.T) {
	bad := &mockProvider{name: "a", models: []string{"gpt-4o"}, err: fmt.Errorf("fail")}

	f := NewFallback(
		[]Target{{VirtualKey: "a"}},
		newLookup(bad),
	).WithMaxRetries(5)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := f.Execute(ctx, providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected error on cancelled context")
	}
}

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
