package plugin

import "testing"

// contentAgnosticMock is a guardrail that declares it reaches its verdict
// without reading request content — the shape max-token has when
// max_input_length is unset.
type contentAgnosticMock struct {
	mockPlugin
	ignores bool
}

func (c *contentAgnosticMock) IgnoresRequestContent() bool { return c.ignores }

// TestHasBeforeRequestGuardrail_IgnoresContentAgnostic pins CORE-007.
//
// A surface that cannot show a guardrail the content it would screen refuses
// rather than read the empty pass as consent. TypeGuardrail names a plugin's
// enforcement role, not what it reads, so that refusal fired for a deployment
// running only a guardrail that never looks at content.
//
// The polarity is the security property under test: a guardrail that declares
// NOTHING still triggers the refusal, so an out-of-tree content guardrail is
// never reclassified into forwarding an unscreened body.
func TestHasBeforeRequestGuardrail_IgnoresContentAgnostic(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    Plugin
		want bool
	}{
		{
			name: "a guardrail declaring nothing still refuses uninspectable content",
			p:    &mockPlugin{name: "word-filter", typ: TypeGuardrail},
			want: true,
		},
		{
			name: "a guardrail that declares it reads no content does not",
			p:    &contentAgnosticMock{mockPlugin{name: "max-token", typ: TypeGuardrail}, true},
			want: false,
		},
		{
			name: "a guardrail that declares it does read content still does",
			p:    &contentAgnosticMock{mockPlugin{name: "max-token", typ: TypeGuardrail}, false},
			want: true,
		},
		{
			name: "a non-guardrail was never counted",
			p:    &mockPlugin{name: "cache", typ: TypeTransform},
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(nil)
			if err := m.Register(StageBeforeRequest, tc.p); err != nil {
				t.Fatalf("register: %v", err)
			}
			if got := m.HasBeforeRequestGuardrail(); got != tc.want {
				t.Errorf("HasBeforeRequestGuardrail() = %v, want %v", got, tc.want)
			}
		})
	}
}

// One content-reading guardrail is enough, however many content-agnostic ones
// are registered alongside it. The predicate answers "is anything here going to
// be denied its input", not "is everything here".
func TestHasBeforeRequestGuardrail_OneContentReaderIsEnough(t *testing.T) {
	m := NewManager(nil)
	for _, p := range []Plugin{
		&contentAgnosticMock{mockPlugin{name: "max-token", typ: TypeGuardrail}, true},
		&mockPlugin{name: "word-filter", typ: TypeGuardrail},
		&contentAgnosticMock{mockPlugin{name: "other", typ: TypeGuardrail}, true},
	} {
		if err := m.Register(StageBeforeRequest, p); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	if !m.HasBeforeRequestGuardrail() {
		t.Error("HasBeforeRequestGuardrail() = false, want true — a content guardrail is registered")
	}
}
