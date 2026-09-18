package plugin

import (
	"context"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

type capturedMatch struct {
	match   GuardrailMatch
	allowed bool
}

// sinkManager wires a manager with a capturing guardrail-match sink.
func sinkManager(t *testing.T) (*Manager, *[]capturedMatch) {
	t.Helper()
	m := NewManager(nil)
	got := &[]capturedMatch{}
	m.SetGuardrailMatchSink(func(_ context.Context, match GuardrailMatch, allowed bool) {
		*got = append(*got, capturedMatch{match: match, allowed: allowed})
	})
	return m, got
}

// TestManager_GuardrailMatch_NonBlockingIsObservable proves the point of the
// feature: a guardrail that matches and resolves to a non-blocking action lets
// the request through AND produces exactly one match signal, attributed to the
// instance and carrying the resolved action.
func TestManager_GuardrailMatch_NonBlockingIsObservable(t *testing.T) {
	m, got := sinkManager(t)
	logGuard := &mockPlugin{name: "regex-guard", typ: TypeGuardrail, execFn: func(_ context.Context, pctx *Context) error {
		pctx.NoteGuardrailMatch(ActionLog)
		return nil
	}}
	if err := m.RegisterWithID(StageBeforeRequest, logGuard, "audit-emails"); err != nil {
		t.Fatal(err)
	}

	pctx := NewContext(&providers.Request{Model: "gpt-4o"})
	if err := m.RunBefore(context.Background(), pctx); err != nil {
		t.Fatalf("request must be allowed under a log action, got %v", err)
	}

	if len(*got) != 1 {
		t.Fatalf("want exactly one match signal, got %d", len(*got))
	}
	c := (*got)[0]
	if c.match.Action != ActionLog || c.match.Plugin != "regex-guard" ||
		c.match.Instance != "audit-emails" || c.match.Stage != StageBeforeRequest || !c.allowed {
		t.Errorf("signal = %+v allowed=%v, want {regex-guard audit-emails before_request log} allowed=true", c.match, c.allowed)
	}
}

// TestManager_GuardrailMatch_NoMatchNoSignal keeps the happy path silent.
func TestManager_GuardrailMatch_NoMatchNoSignal(t *testing.T) {
	m, got := sinkManager(t)
	quiet := &mockPlugin{name: "regex-guard", typ: TypeGuardrail} // never matches
	if err := m.Register(StageBeforeRequest, quiet); err != nil {
		t.Fatal(err)
	}

	pctx := NewContext(&providers.Request{Model: "gpt-4o"})
	if err := m.RunBefore(context.Background(), pctx); err != nil {
		t.Fatalf("RunBefore: %v", err)
	}
	if len(*got) != 0 {
		t.Fatalf("a non-matching plugin must emit nothing, got %d signals", len(*got))
	}
}

// TestManager_GuardrailMatch_BlockStillSignals verifies a block emits one signal
// with allowed=false, unchanged rejection behaviour otherwise.
func TestManager_GuardrailMatch_BlockStillSignals(t *testing.T) {
	m, got := sinkManager(t)
	blockGuard := &mockPlugin{name: "word-filter", typ: TypeGuardrail, execFn: func(_ context.Context, pctx *Context) error {
		pctx.NoteGuardrailMatch(ActionBlock)
		pctx.Reject = true
		return nil
	}}
	if err := m.RegisterWithID(StageBeforeRequest, blockGuard, "deny-secrets"); err != nil {
		t.Fatal(err)
	}

	pctx := NewContext(&providers.Request{Model: "gpt-4o"})
	if err := m.RunBefore(context.Background(), pctx); err == nil {
		t.Fatal("a block must still reject")
	}
	if len(*got) != 1 {
		t.Fatalf("want one match signal for a block, got %d", len(*got))
	}
	c := (*got)[0]
	if c.match.Action != ActionBlock || c.allowed {
		t.Errorf("signal = %+v allowed=%v, want block/allowed=false", c.match, c.allowed)
	}
}

// TestManager_GuardrailMatch_OneSignalPerActionPerInvocation bounds the volume: a
// warn/log rule is evaluated on every piece of text, so a long conversation must
// not become one identical event per message.
func TestManager_GuardrailMatch_OneSignalPerActionPerInvocation(t *testing.T) {
	m, got := sinkManager(t)
	chatty := &mockPlugin{name: "regex-guard", typ: TypeGuardrail, execFn: func(_ context.Context, pctx *Context) error {
		for range 50 { // fifty messages, each matching the same log rule
			pctx.NoteGuardrailMatch(ActionLog)
		}
		pctx.NoteGuardrailMatch(ActionWarn) // a second rule with a different action
		return nil
	}}
	if err := m.Register(StageBeforeRequest, chatty); err != nil {
		t.Fatal(err)
	}

	pctx := NewContext(&providers.Request{Model: "gpt-4o"})
	if err := m.RunBefore(context.Background(), pctx); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 2 || (*got)[0].match.Action != ActionLog || (*got)[1].match.Action != ActionWarn {
		t.Fatalf("want exactly [log warn], got %+v", *got)
	}
}

// TestManager_GuardrailMatch_AfterStageAndFlushSeparation checks the stage is
// stamped correctly and that a match in one stage is not re-reported by the next.
func TestManager_GuardrailMatch_AfterStageAndFlushSeparation(t *testing.T) {
	m, got := sinkManager(t)
	afterGuard := &mockPlugin{name: "schema-guard", typ: TypeGuardrail, execFn: func(_ context.Context, pctx *Context) error {
		if pctx.Stage == StageAfterRequest {
			pctx.NoteGuardrailMatch(ActionWarn)
		}
		return nil
	}}
	if err := m.Register(StageAfterRequest, afterGuard); err != nil {
		t.Fatal(err)
	}

	pctx := NewContext(&providers.Request{Model: "gpt-4o"})
	// A before stage with the same context adds nothing; the after stage adds one.
	if err := m.RunBefore(context.Background(), pctx); err != nil {
		t.Fatal(err)
	}
	if err := m.RunAfter(context.Background(), pctx); err != nil {
		t.Fatal(err)
	}

	if len(*got) != 1 {
		t.Fatalf("want one after-stage signal, got %d", len(*got))
	}
	if (*got)[0].match.Stage != StageAfterRequest || (*got)[0].match.Action != ActionWarn {
		t.Errorf("signal = %+v, want after_request/warn", (*got)[0].match)
	}
}
