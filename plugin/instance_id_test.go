package plugin

import (
	"context"
	"errors"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

// TestManager_RejectionCarriesInstanceID verifies a rejection is attributed to
// the configured instance that produced it, not merely to the plugin type, when
// several instances of one plugin are registered — the whole point of the id.
func TestManager_RejectionCarriesInstanceID(t *testing.T) {
	allow := &mockPlugin{name: "pii-redact", typ: TypeGuardrail}
	block := &mockPlugin{name: "pii-redact", typ: TypeGuardrail, execFn: func(_ context.Context, pctx *Context) error {
		pctx.Reject = true
		pctx.Reason = "blocked"
		return nil
	}}

	m := NewManager(nil)
	if err := m.RegisterWithID(StageBeforeRequest, allow, "allow-emails"); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterWithID(StageBeforeRequest, block, "block-ssn"); err != nil {
		t.Fatal(err)
	}

	pctx := NewContext(&providers.Request{Model: "gpt-4o"})
	err := m.RunBefore(context.Background(), pctx)

	var rej *RejectionError
	if !errors.As(err, &rej) {
		t.Fatalf("want *RejectionError, got %T: %v", err, err)
	}
	if rej.Instance != "block-ssn" {
		t.Errorf("Instance = %q, want %q (the rejecting instance, not the other)", rej.Instance, "block-ssn")
	}
	if rej.Plugin != "pii-redact" {
		t.Errorf("Plugin = %q, want pii-redact", rej.Plugin)
	}
}

// TestManager_RejectionWithoutInstanceID verifies an instance registered without
// an id rejects with an empty Instance and otherwise unchanged behaviour, so a
// config that names no id is unaffected.
func TestManager_RejectionWithoutInstanceID(t *testing.T) {
	block := &mockPlugin{name: "word-filter", typ: TypeGuardrail, execFn: func(_ context.Context, pctx *Context) error {
		pctx.Reject = true
		return nil
	}}
	m := NewManager(nil)
	if err := m.Register(StageBeforeRequest, block); err != nil { // no id
		t.Fatal(err)
	}

	pctx := NewContext(&providers.Request{Model: "gpt-4o"})
	err := m.RunBefore(context.Background(), pctx)

	var rej *RejectionError
	if !errors.As(err, &rej) {
		t.Fatalf("want *RejectionError, got %T: %v", err, err)
	}
	if rej.Instance != "" {
		t.Errorf("Instance = %q, want empty for an instance registered without an id", rej.Instance)
	}
}

// valuePlugin is a plugin used by VALUE whose struct is not hashable. Nothing in
// the Plugin contract forbids the shape, and uniquePluginInstances tolerates it.
type valuePlugin struct{ words []string }

func (valuePlugin) Name() string              { return "value-plugin" }
func (valuePlugin) Type() PluginType          { return TypeGuardrail }
func (valuePlugin) Init(map[string]any) error { return nil }
func (valuePlugin) Close() error              { return nil }
func (valuePlugin) Execute(_ context.Context, pctx *Context) error {
	pctx.NoteGuardrailMatch(ActionBlock)
	pctx.Reject = true
	return nil
}

// TestManager_UnhashableValuePluginDoesNotPanic guards the id lookup against a
// value-typed plugin holding a slice: keyed by the Plugin value, the lookup
// panics with "hash of unhashable type" on every rejection — outside
// executePlugin's recover — even when no id was ever configured.
func TestManager_UnhashableValuePluginDoesNotPanic(t *testing.T) {
	m := NewManager(nil)
	if err := m.RegisterWithID(StageBeforeRequest, valuePlugin{words: []string{"x"}}, "some-id"); err != nil {
		t.Fatal(err)
	}

	pctx := NewContext(&providers.Request{Model: "gpt-4o"})
	err := m.RunBefore(context.Background(), pctx)

	var rej *RejectionError
	if !errors.As(err, &rej) {
		t.Fatalf("want *RejectionError, got %T: %v", err, err)
	}
	if rej.Instance != "" {
		t.Errorf("Instance = %q; a non-pointer plugin has no identity to carry an id", rej.Instance)
	}
}
