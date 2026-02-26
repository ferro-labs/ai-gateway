package providers

import (
	"context"
	"testing"
)

type stubProvider struct {
	name   string
	models []string
}

func (s *stubProvider) Name() string              { return s.name }
func (s *stubProvider) SupportedModels() []string  { return s.models }
func (s *stubProvider) SupportsModel(m string) bool {
	for _, mm := range s.models {
		if mm == m {
			return true
		}
	}
	return false
}
func (s *stubProvider) Models() []ModelInfo {
	out := make([]ModelInfo, len(s.models))
	for i, m := range s.models {
		out[i] = ModelInfo{ID: m, Object: "model", OwnedBy: s.name}
	}
	return out
}
func (s *stubProvider) Complete(_ context.Context, _ Request) (*Response, error) {
	return &Response{ID: "stub"}, nil
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubProvider{name: "a", models: []string{"m1"}})

	p, ok := r.Get("a")
	if !ok {
		t.Fatal("expected provider a")
	}
	if p.Name() != "a" {
		t.Errorf("got %q", p.Name())
	}

	_, ok = r.Get("missing")
	if ok {
		t.Error("expected not found")
	}
}

func TestRegistry_List(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubProvider{name: "x"})
	r.Register(&stubProvider{name: "y"})

	names := r.List()
	if len(names) != 2 {
		t.Errorf("got %d names, want 2", len(names))
	}
}

func TestRegistry_FindByModel(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubProvider{name: "a", models: []string{"gpt-4o"}})
	r.Register(&stubProvider{name: "b", models: []string{"claude-3"}})

	p, ok := r.FindByModel("claude-3")
	if !ok {
		t.Fatal("expected to find claude-3")
	}
	if p.Name() != "b" {
		t.Errorf("got %q, want b", p.Name())
	}

	_, ok = r.FindByModel("unknown")
	if ok {
		t.Error("expected not found for unknown model")
	}
}

func TestRegistry_AllModels(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubProvider{name: "a", models: []string{"m1", "m2"}})
	r.Register(&stubProvider{name: "b", models: []string{"m3"}})

	models := r.AllModels()
	if len(models) != 3 {
		t.Errorf("got %d models, want 3", len(models))
	}
}
