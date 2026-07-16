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
func (s *stubProvider) SupportedModels() []string { return s.models }
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

func TestRegistry_RegisterAs_DistinctAliasesSameName(t *testing.T) {
	r := NewRegistry()
	r.RegisterAs("ollama-cloud-a", "ollama-cloud", &stubProvider{name: "ollama-cloud", models: []string{"m-a"}})
	r.RegisterAs("ollama-cloud-b", "ollama-cloud", &stubProvider{name: "ollama-cloud", models: []string{"m-b"}})

	pa, ok := r.Get("ollama-cloud-a")
	if !ok {
		t.Fatal("expected ollama-cloud-a to be registered")
	}
	pb, ok := r.Get("ollama-cloud-b")
	if !ok {
		t.Fatal("expected ollama-cloud-b to be registered")
	}

	if !pa.SupportsModel("m-a") {
		t.Error("expected ollama-cloud-a instance to support m-a")
	}
	if pa.SupportsModel("m-b") {
		t.Error("did not expect ollama-cloud-a instance to support m-b (would indicate clobbering)")
	}
	if !pb.SupportsModel("m-b") {
		t.Error("expected ollama-cloud-b instance to support m-b")
	}
	if pb.SupportsModel("m-a") {
		t.Error("did not expect ollama-cloud-b instance to support m-a (would indicate clobbering)")
	}

	names := r.List()
	if len(names) != 2 {
		t.Fatalf("got %d registered names, want 2 (both aliases retained independently): %v", len(names), names)
	}
}

func TestRegistry_CanonicalType(t *testing.T) {
	r := NewRegistry()
	r.RegisterAs("ollama-cloud-a", "ollama-cloud", &stubProvider{name: "ollama-cloud"})
	r.Register(&stubProvider{name: "openai"})

	canonicalType, ok := r.CanonicalType("ollama-cloud-a")
	if !ok {
		t.Fatal("expected ollama-cloud-a to have a known canonical type")
	}
	if canonicalType != "ollama-cloud" {
		t.Errorf("got %q, want %q", canonicalType, "ollama-cloud")
	}

	// Register(p) is a pure refactor of RegisterAs(p.Name(), p.Name(), p): a
	// name registered the plain way is its own canonical type.
	canonicalType, ok = r.CanonicalType("openai")
	if !ok {
		t.Fatal("expected openai to have a known canonical type")
	}
	if canonicalType != "openai" {
		t.Errorf("got %q, want %q", canonicalType, "openai")
	}

	// Unknown alias: falls back to returning the input unchanged with ok=false.
	canonicalType, ok = r.CanonicalType("never-registered")
	if ok {
		t.Error("expected ok=false for an alias that was never registered")
	}
	if canonicalType != "never-registered" {
		t.Errorf("got %q, want input returned unchanged", canonicalType)
	}
}
