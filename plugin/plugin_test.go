package plugin

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

type mockPlugin struct {
	name    string
	typ     PluginType
	execFn  func(ctx context.Context, pctx *Context) error
	initErr error
}

func (m *mockPlugin) Name() string                        { return m.name }
func (m *mockPlugin) Type() PluginType                    { return m.typ }
func (m *mockPlugin) Init(_ map[string]interface{}) error { return m.initErr }
func (m *mockPlugin) Execute(ctx context.Context, pctx *Context) error {
	if m.execFn != nil {
		return m.execFn(ctx, pctx)
	}
	return nil
}

func TestNewContext(t *testing.T) {
	req := &providers.Request{Model: "gpt-4o"}
	pctx := NewContext(req)
	if pctx.Request.Model != "gpt-4o" {
		t.Errorf("got model %q", pctx.Request.Model)
	}
	if pctx.Metadata == nil {
		t.Error("Metadata should be initialized")
	}
}

func TestManager_Register(t *testing.T) {
	m := NewManager()
	p := &mockPlugin{name: "test", typ: TypeGuardrail}

	if err := m.Register(StageBeforeRequest, p); err != nil {
		t.Fatal(err)
	}
	if !m.HasPlugins() {
		t.Error("expected HasPlugins=true")
	}

	if err := m.Register("invalid", p); err == nil {
		t.Error("expected error for invalid stage")
	}
}

func TestManager_RunBefore(t *testing.T) {
	m := NewManager()
	called := false
	_ = m.Register(StageBeforeRequest, &mockPlugin{
		name: "track",
		typ:  TypeGuardrail,
		execFn: func(_ context.Context, _ *Context) error {
			called = true
			return nil
		},
	})

	pctx := NewContext(&providers.Request{Model: "gpt-4o"})
	if err := m.RunBefore(context.Background(), pctx); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("plugin was not called")
	}
}

func TestManager_RunBefore_Reject(t *testing.T) {
	m := NewManager()
	_ = m.Register(StageBeforeRequest, &mockPlugin{
		name: "blocker",
		typ:  TypeGuardrail,
		execFn: func(_ context.Context, pctx *Context) error {
			pctx.Reject = true
			pctx.Reason = "blocked"
			return nil
		},
	})

	pctx := NewContext(&providers.Request{Model: "gpt-4o"})
	err := m.RunBefore(context.Background(), pctx)
	if err == nil {
		t.Fatal("expected rejection error")
	}

	var rejection *RejectionError
	if !errors.As(err, &rejection) {
		t.Fatalf("expected RejectionError, got %T", err)
	}
}

func TestManager_RunBefore_RejectWithError_ReturnsRejectionError(t *testing.T) {
	m := NewManager()
	_ = m.Register(StageBeforeRequest, &mockPlugin{
		name: "rate-limit",
		typ:  TypeRateLimit,
		execFn: func(_ context.Context, pctx *Context) error {
			pctx.Reject = true
			pctx.Reason = "rate limit exceeded"
			return fmt.Errorf("rate limit exceeded")
		},
	})

	pctx := NewContext(&providers.Request{Model: "gpt-4o"})
	err := m.RunBefore(context.Background(), pctx)
	if err == nil {
		t.Fatal("expected rejection error")
	}

	var rejection *RejectionError
	if !errors.As(err, &rejection) {
		t.Fatalf("expected RejectionError, got %T", err)
	}
	if rejection.Stage != StageBeforeRequest {
		t.Fatalf("stage = %q, want %q", rejection.Stage, StageBeforeRequest)
	}
	if rejection.Reason != "rate limit exceeded" {
		t.Fatalf("reason = %q, want %q", rejection.Reason, "rate limit exceeded")
	}
}

func TestManager_RunBefore_RejectWithError_FallsBackToErrorMessage(t *testing.T) {
	m := NewManager()
	_ = m.Register(StageBeforeRequest, &mockPlugin{
		name: "guardrail",
		typ:  TypeGuardrail,
		execFn: func(_ context.Context, pctx *Context) error {
			pctx.Reject = true
			return fmt.Errorf("blocked by policy")
		},
	})

	pctx := NewContext(&providers.Request{Model: "gpt-4o"})
	err := m.RunBefore(context.Background(), pctx)
	if err == nil {
		t.Fatal("expected rejection error")
	}

	var rejection *RejectionError
	if !errors.As(err, &rejection) {
		t.Fatalf("expected RejectionError, got %T", err)
	}
	if rejection.Reason != "blocked by policy" {
		t.Fatalf("reason = %q, want %q", rejection.Reason, "blocked by policy")
	}
}

func TestManager_RunAfter(t *testing.T) {
	m := NewManager()
	called := false
	_ = m.Register(StageAfterRequest, &mockPlugin{
		name: "logger",
		typ:  TypeLogging,
		execFn: func(_ context.Context, _ *Context) error {
			called = true
			return nil
		},
	})

	pctx := NewContext(&providers.Request{})
	pctx.Response = &providers.Response{ID: "r1"}
	_ = m.RunAfter(context.Background(), pctx)
	if !called {
		t.Error("after plugin was not called")
	}
}

func TestManager_RunAfter_RejectWithEmptyReason_UsesDefault(t *testing.T) {
	m := NewManager()
	_ = m.Register(StageAfterRequest, &mockPlugin{
		name: "post-guardrail",
		typ:  TypeGuardrail,
		execFn: func(_ context.Context, pctx *Context) error {
			pctx.Reject = true
			return nil
		},
	})

	pctx := NewContext(&providers.Request{})
	pctx.Response = &providers.Response{ID: "r1"}
	err := m.RunAfter(context.Background(), pctx)
	if err == nil {
		t.Fatal("expected rejection error")
	}

	var rejection *RejectionError
	if !errors.As(err, &rejection) {
		t.Fatalf("expected RejectionError, got %T", err)
	}
	if rejection.Reason != "rejected" {
		t.Fatalf("reason = %q, want %q", rejection.Reason, "rejected")
	}
}

func TestManager_RunAfter_RejectWithErrorAndEmptyReason_UsesErrorMessage(t *testing.T) {
	m := NewManager()
	_ = m.Register(StageAfterRequest, &mockPlugin{
		name: "schema-guard",
		typ:  TypeGuardrail,
		execFn: func(_ context.Context, pctx *Context) error {
			pctx.Reject = true
			return fmt.Errorf("schema mismatch")
		},
	})

	pctx := NewContext(&providers.Request{})
	pctx.Response = &providers.Response{ID: "r1"}
	err := m.RunAfter(context.Background(), pctx)
	if err == nil {
		t.Fatal("expected rejection error")
	}

	var rejection *RejectionError
	if !errors.As(err, &rejection) {
		t.Fatalf("expected RejectionError, got %T", err)
	}
	if rejection.Reason != "schema mismatch" {
		t.Fatalf("reason = %q, want %q", rejection.Reason, "schema mismatch")
	}
}

func TestManager_NoPlugins(t *testing.T) {
	m := NewManager()
	if m.HasPlugins() {
		t.Error("expected HasPlugins=false")
	}
	pctx := NewContext(&providers.Request{})
	if err := m.RunBefore(context.Background(), pctx); err != nil {
		t.Fatal(err)
	}
}
