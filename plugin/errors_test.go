package plugin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

func TestRejectionError_Error_BeforeRequest(t *testing.T) {
	err := (&RejectionError{
		Plugin: "guardrail-a",
		Stage:  StageBeforeRequest,
		Reason: "blocked input",
	}).Error()

	want := "request rejected by guardrail-a (before_request): blocked input"
	if err != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
}

func TestRejectionError_Error_AfterRequest(t *testing.T) {
	err := (&RejectionError{
		Plugin: "guardrail-b",
		Stage:  StageAfterRequest,
		Reason: "schema mismatch",
	}).Error()

	want := "response rejected by guardrail-b (after_request): schema mismatch"
	if err != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
}

func TestRejectionError_Error_UnknownStage(t *testing.T) {
	err := (&RejectionError{
		Plugin: "guardrail-c",
		Stage:  Stage("custom_stage"),
		Reason: "custom",
	}).Error()

	want := "rejected by guardrail-c (custom_stage): custom"
	if err != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
}

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
			m := NewManager(nil)
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
			m := NewManager(nil)
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

func TestRunBefore_RejectIsHonoredForEveryPluginType(t *testing.T) {
	// A plugin only sets Reject when it has DECIDED to deny the request. The
	// public Context.Reject contract says that aborts the request; silently
	// discarding the decision for some plugin types would make that a lie —
	// and would let a transform plugin's refusal to sanitize sail through.
	for _, typ := range []PluginType{TypeGuardrail, TypeRateLimit, TypeAuth, TypeTransform, TypeLogging, TypeMetrics} {
		t.Run(string(typ), func(t *testing.T) {
			m := NewManager(nil)
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

func TestRunAfter_PluginErrorIsAFailureNotARejection(t *testing.T) {
	m := NewManager(nil)
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
