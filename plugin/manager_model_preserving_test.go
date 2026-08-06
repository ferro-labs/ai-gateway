package plugin

import "testing"

// modelPreservingMock is a transform that declares it cannot change which model
// a request routes to — the shape the response cache has.
type modelPreservingMock struct{ mockPlugin }

func (*modelPreservingMock) PreservesModel() {}

// TestHasBeforeRequestTransform_IgnoresModelPreserving pins PLUGIN-011.
//
// The admission check stands down when a before_request transform could still
// rewrite the model. The response cache reports TypeTransform because it
// short-circuits the provider call, not because it rewrites anything, so
// enabling it used to turn that check off for the whole process — and an
// unroutable model then spent a rate limiter's tokens and a budget's money on
// its way to a 404 it was always going to get.
//
// The declaration is an opt-out: a transform that says nothing keeps the
// stand-down, so a plugin built outside this repository is unaffected.
func TestHasBeforeRequestTransform_IgnoresModelPreserving(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    Plugin
		want bool
	}{
		{
			name: "a transform declaring nothing still stands admission down",
			p:    &mockPlugin{name: "rewriter", typ: TypeTransform},
			want: true,
		},
		{
			name: "a model-preserving transform does not",
			p:    &modelPreservingMock{mockPlugin{name: "cache", typ: TypeTransform}},
			want: false,
		},
		{
			name: "a guardrail was never a transform",
			p:    &mockPlugin{name: "filter", typ: TypeGuardrail},
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(nil)
			if err := m.Register(StageBeforeRequest, tc.p); err != nil {
				t.Fatalf("register: %v", err)
			}
			if got := m.HasBeforeRequestTransform(); got != tc.want {
				t.Errorf("HasBeforeRequestTransform() = %v, want %v", got, tc.want)
			}
		})
	}
}
