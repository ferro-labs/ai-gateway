package strategies

import (
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

// A Single strategy has exactly one answer, and it does not depend on the
// request. Everything else the old Execute tests covered — provider not found,
// unsupported model, error propagation, stamping the provider on the response —
// is the request pipeline's, and is tested there against all four surfaces at
// once instead of once per strategy.
func TestSingle_SelectTargets(t *testing.T) {
	s := NewSingle(Target{VirtualKey: "a"})
	keys, err := s.SelectTargets(providers.Request{Model: "gpt-4o"})
	if err != nil {
		t.Fatal(err)
	}
	assertKeys(t, keys, "a")

	// The model is irrelevant: a single-target config commits before it looks.
	keys, err = s.SelectTargets(providers.Request{Model: "claude-3"})
	if err != nil {
		t.Fatal(err)
	}
	assertKeys(t, keys, "a")
}
