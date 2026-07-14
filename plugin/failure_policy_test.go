package plugin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

// A plugin that BREAKS and a plugin that DENIES are different events, and the
// gateway must not report one as the other. These tests pin that separation:
// an error or panic yields a FailureError (the gateway malfunctioned), while
// Context.Reject yields a RejectionError (the request was denied on purpose).

func TestRunBefore_PluginErrorIsAFailureNotARejection(t *testing.T) {
	tests := []struct {
		name string
		typ  PluginType
	}{
		{"guardrail", TypeGuardrail},
		{"ratelimit", TypeRateLimit},
		{"auth", TypeAuth},
		{"transform", TypeTransform},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewManager()
			backendDown := errors.New("backend unavailable")
			_ = m.Register(StageBeforeRequest, &mockPlugin{
				name: "broken",
				typ:  tt.typ,
				execFn: func(context.Context, *Context) error {
					return backendDown
				},
			})

			err := m.RunBefore(context.Background(), NewContext(&providers.Request{}))
			if err == nil {
				t.Fatal("a fail-closed plugin that errors must abort the request")
			}

			var failure *FailureError
			if !errors.As(err, &failure) {
				t.Fatalf("a broken plugin must surface as *FailureError, got %T: %v", err, err)
			}
			var rejection *RejectionError
			if errors.As(err, &rejection) {
				t.Fatal("a broken plugin must NOT surface as a RejectionError: the request was never denied, the gateway simply could not evaluate it")
			}
			if !errors.Is(err, backendDown) {
				t.Error("FailureError must unwrap to the plugin's own error so callers can inspect the cause")
			}
			if failure.Plugin != "broken" || failure.PluginType != tt.typ || failure.Stage != StageBeforeRequest {
				t.Errorf("failure = %+v, want the plugin, type, and stage that failed", failure)
			}
		})
	}
}

func TestPanicIsAFailureNotARejection(t *testing.T) {
	stages := []struct {
		stage Stage
		run   func(*Manager, *Context) error
	}{
		{StageBeforeRequest, func(m *Manager, pctx *Context) error { return m.RunBefore(context.Background(), pctx) }},
		{StageAfterRequest, func(m *Manager, pctx *Context) error { return m.RunAfter(context.Background(), pctx) }},
	}

	for _, s := range stages {
		t.Run(string(s.stage), func(t *testing.T) {
			m := NewManager()
			_ = m.Register(s.stage, &mockPlugin{
				name: "panicky",
				typ:  TypeGuardrail,
				execFn: func(context.Context, *Context) error {
					panic("nil map write")
				},
			})

			pctx := NewContext(&providers.Request{})
			pctx.Response = &providers.Response{ID: "r1"}
			err := s.run(m, pctx)

			var failure *FailureError
			if !errors.As(err, &failure) {
				t.Fatalf("a panicking plugin must surface as *FailureError, got %T: %v", err, err)
			}
			if !strings.Contains(err.Error(), "plugin panicky panicked") {
				t.Errorf("error = %q, want it to name the plugin that panicked", err.Error())
			}
			if strings.Contains(err.Error(), "runtime/debug.Stack") {
				t.Errorf("error = %q, must not leak the panic stack to the client", err.Error())
			}
		})
	}
}

func TestUnknownPluginTypeFailsClosed(t *testing.T) {
	// A third-party plugin type the gateway has never heard of gates the request
	// by default. Failing open on an unrecognised type would mean a typo in
	// `type:` silently disables a guardrail.
	m := NewManager()
	_ = m.Register(StageAfterRequest, &mockPlugin{
		name: "custom-policy",
		typ:  PluginType("custom"),
		execFn: func(context.Context, *Context) error {
			return errors.New("custom plugin failed")
		},
	})

	pctx := NewContext(&providers.Request{})
	pctx.Response = &providers.Response{ID: "r1"}

	var failure *FailureError
	if err := m.RunAfter(context.Background(), pctx); !errors.As(err, &failure) {
		t.Fatalf("an unknown plugin type must fail closed with *FailureError, got %T: %v", err, err)
	}
	if failure.PluginType != PluginType("custom") {
		t.Errorf("plugin type = %q, want custom", failure.PluginType)
	}
}

func TestRunBefore_RejectIsHonoredForEveryPluginType(t *testing.T) {
	// A plugin only sets Reject when it has DECIDED to deny the request. The
	// public Context.Reject contract says that aborts the request; silently
	// discarding the decision for some plugin types would make that a lie —
	// and would let a transform plugin's refusal to sanitize sail through.
	for _, typ := range []PluginType{TypeGuardrail, TypeRateLimit, TypeAuth, TypeTransform, TypeLogging, TypeMetrics} {
		t.Run(string(typ), func(t *testing.T) {
			m := NewManager()
			_ = m.Register(StageBeforeRequest, &mockPlugin{
				name: "decider",
				typ:  typ,
				execFn: func(_ context.Context, pctx *Context) error {
					pctx.Reject = true
					pctx.Reason = "denied on purpose"
					return nil
				},
			})

			err := m.RunBefore(context.Background(), NewContext(&providers.Request{}))
			var rejection *RejectionError
			if !errors.As(err, &rejection) {
				t.Fatalf("Reject must abort the request for a %s plugin, got %T: %v", typ, err, err)
			}
			if rejection.Reason != "denied on purpose" {
				t.Errorf("reason = %q, want the plugin's own reason", rejection.Reason)
			}
		})
	}
}

func TestRunBefore_ObservabilityPluginErrorsFailOpen(t *testing.T) {
	// A broken log sink or metrics backend must never take down the request
	// path: it observes the request, it does not gate it.
	for _, typ := range []PluginType{TypeLogging, TypeMetrics} {
		t.Run(string(typ), func(t *testing.T) {
			m := NewManager()
			_ = m.Register(StageBeforeRequest, &mockPlugin{
				name: "observer",
				typ:  typ,
				execFn: func(context.Context, *Context) error {
					return fmt.Errorf("sink down")
				},
			})
			reached := false
			_ = m.Register(StageBeforeRequest, &mockPlugin{
				name: "next",
				typ:  TypeGuardrail,
				execFn: func(context.Context, *Context) error {
					reached = true
					return nil
				},
			})

			if err := m.RunBefore(context.Background(), NewContext(&providers.Request{})); err != nil {
				t.Fatalf("a %s plugin error must not abort the request, got %v", typ, err)
			}
			if !reached {
				t.Fatal("a fail-open plugin error must not skip the plugins after it")
			}
		})
	}
}

func TestRunAfter_PluginErrorIsAFailureNotARejection(t *testing.T) {
	m := NewManager()
	_ = m.Register(StageAfterRequest, &mockPlugin{
		name: "broken",
		typ:  TypeGuardrail,
		execFn: func(context.Context, *Context) error {
			return errors.New("scanner unavailable")
		},
	})

	pctx := NewContext(&providers.Request{})
	pctx.Response = &providers.Response{ID: "r1"}

	err := m.RunAfter(context.Background(), pctx)
	var failure *FailureError
	if !errors.As(err, &failure) {
		t.Fatalf("a broken after-request plugin must surface as *FailureError, got %T: %v", err, err)
	}
	if failure.Stage != StageAfterRequest {
		t.Errorf("stage = %q, want %q", failure.Stage, StageAfterRequest)
	}
}
