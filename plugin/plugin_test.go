package plugin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/providers"
)

type mockPlugin struct {
	name    string
	typ     PluginType
	execFn  func(ctx context.Context, pctx *Context) error
	closeFn func() error
	initErr error
}

func (m *mockPlugin) Name() string                { return m.name }
func (m *mockPlugin) Type() PluginType            { return m.typ }
func (m *mockPlugin) Init(_ map[string]any) error { return m.initErr }
func (m *mockPlugin) Execute(ctx context.Context, pctx *Context) error {
	if m.execFn != nil {
		return m.execFn(ctx, pctx)
	}
	return nil
}
func (m *mockPlugin) Close() error {
	if m.closeFn != nil {
		return m.closeFn()
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

func TestManager_RunBefore_PanicReturnsError(t *testing.T) {
	m := NewManager()
	_ = m.Register(StageBeforeRequest, &mockPlugin{
		name: "panic-guard",
		typ:  TypeGuardrail,
		execFn: func(context.Context, *Context) error {
			panic("boom")
		},
	})

	pctx := NewContext(&providers.Request{Model: "gpt-4o"})
	err := m.RunBefore(context.Background(), pctx)
	if err == nil {
		t.Fatal("expected panic to be converted to an error")
	}
	if !strings.Contains(err.Error(), "plugin panic-guard panicked") {
		t.Fatalf("error = %q, want plugin panic context", err.Error())
	}
	if strings.Contains(err.Error(), "runtime/debug.Stack") {
		t.Fatalf("error = %q, should not expose panic stack", err.Error())
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

func TestManager_RunOnError_PanicDoesNotPropagate(t *testing.T) {
	m := NewManager()
	called := false
	_ = m.Register(StageOnError, &mockPlugin{
		name: "panic-reporter",
		typ:  TypeLogging,
		execFn: func(context.Context, *Context) error {
			called = true
			panic("reporter failed")
		},
	})

	m.RunOnError(context.Background(), NewContext(&providers.Request{Model: "gpt-4o"}))
	if !called {
		t.Fatal("expected on-error plugin to run")
	}
}

func TestManager_CloseClosesRegisteredPlugins(t *testing.T) {
	m := NewManager()
	var closed int
	_ = m.Register(StageBeforeRequest, &mockPlugin{
		name: "closer",
		typ:  TypeLogging,
		closeFn: func() error {
			closed++
			return nil
		},
	})

	if err := m.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
	if closed != 1 {
		t.Fatalf("closed = %d, want 1", closed)
	}
}

func TestManager_CloseCallsCloseOncePerInstance(t *testing.T) {
	m := NewManager()
	var closed int
	p := &mockPlugin{
		name: "multi-stage",
		typ:  TypeLogging,
		closeFn: func() error {
			closed++
			return nil
		},
	}
	_ = m.Register(StageBeforeRequest, p)
	_ = m.Register(StageAfterRequest, p)

	if err := m.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
	if closed != 1 {
		t.Fatalf("closed = %d, want 1 because Close is deduplicated per plugin instance", closed)
	}
}

func TestManager_CloseClosesDistinctInstances(t *testing.T) {
	m := NewManager()
	var closed int
	for _, name := range []string{"first", "second"} {
		_ = m.Register(StageBeforeRequest, &mockPlugin{
			name: name,
			typ:  TypeLogging,
			closeFn: func() error {
				closed++
				return nil
			},
		})
	}

	if err := m.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
	if closed != 2 {
		t.Fatalf("closed = %d, want 2 distinct instances closed", closed)
	}
}

func TestManager_CloseReturnsWhileActiveAndClosesAfterRelease(t *testing.T) {
	m := NewManager()
	closed := make(chan struct{})
	var closeOnce sync.Once
	_ = m.Register(StageAfterRequest, &mockPlugin{
		name: "deferred-close",
		typ:  TypeLogging,
		closeFn: func() error {
			closeOnce.Do(func() { close(closed) })
			return nil
		},
	})
	release := m.Acquire()

	closeReturned := make(chan error, 1)
	go func() {
		closeReturned <- m.Close()
	}()

	select {
	case err := <-closeReturned:
		if err != nil {
			t.Fatalf("Close() error: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Close blocked while manager was active")
	}
	select {
	case <-closed:
		t.Fatal("plugin closed before active manager user released")
	default:
	}

	release()

	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for deferred plugin close")
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

// TestManager_Register_AllStages verifies that HasPlugins reports true once a
// plugin is registered at any stage and that each stage is independent.
func TestManager_Register_AllStages(t *testing.T) {
	for _, stage := range []Stage{StageBeforeRequest, StageAfterRequest, StageOnError} {
		m := NewManager()
		if m.HasPlugins() {
			t.Fatalf("stage %s: expected HasPlugins=false before register", stage)
		}
		if err := m.Register(stage, &mockPlugin{name: "p", typ: TypeGuardrail}); err != nil {
			t.Fatalf("stage %s: Register() error: %v", stage, err)
		}
		if !m.HasPlugins() {
			t.Errorf("stage %s: expected HasPlugins=true after register", stage)
		}
	}
}

// TestManager_RunBefore_SnapshotIsolation verifies that a plugin registered
// after RunBefore takes its slice snapshot is not included in that run.
// This checks the snapshot semantics introduced by the RLock fix.
func TestManager_RunBefore_SnapshotIsolation(t *testing.T) {
	m := NewManager()

	// firstStarted is closed by the first plugin's Execute, proving that
	// RunBefore has already taken the slice snapshot and entered the loop.
	firstStarted := make(chan struct{})
	gate := make(chan struct{})
	secondCalled := false

	var firstOnce sync.Once
	_ = m.Register(StageBeforeRequest, &mockPlugin{
		name: "first",
		typ:  TypeGuardrail,
		execFn: func(_ context.Context, _ *Context) error {
			// Only coordinate on the first call; subsequent runs just pass through.
			firstOnce.Do(func() {
				close(firstStarted) // snapshot was taken; we are inside the loop
				<-gate              // block until the test has registered the second plugin
			})
			return nil
		},
	})

	done := make(chan error, 1)
	go func() {
		done <- m.RunBefore(context.Background(), NewContext(&providers.Request{Model: "gpt-4o"}))
	}()

	// Wait until the first plugin is executing — the snapshot is now fixed.
	<-firstStarted

	// Register a second plugin. Because the snapshot was already taken, this
	// registration must not affect the current RunBefore call.
	_ = m.Register(StageBeforeRequest, &mockPlugin{
		name: "second",
		typ:  TypeGuardrail,
		execFn: func(_ context.Context, _ *Context) error {
			secondCalled = true
			return nil
		},
	})

	close(gate) // let the first plugin finish
	if err := <-done; err != nil {
		t.Fatalf("RunBefore() error: %v", err)
	}
	if secondCalled {
		t.Error("second plugin (registered after snapshot) must not be called in this run")
	}

	// A subsequent RunBefore takes a fresh snapshot that includes "second".
	_ = m.RunBefore(context.Background(), NewContext(&providers.Request{Model: "gpt-4o"}))
	if !secondCalled {
		t.Error("subsequent RunBefore must execute the newly registered second plugin")
	}
}
