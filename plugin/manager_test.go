package plugin

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/ferro-labs/ai-gateway/observability"
	"github.com/ferro-labs/ai-gateway/providers"
)

// recordingSpan is a test observability.Span that records the child spans it
// spawns along with their attributes, error, and End calls. It lets the tests
// assert that the plugin manager routes per-plugin telemetry through the
// observability seam rather than reaching into any internal package.
type recordingSpan struct {
	mu       sync.Mutex
	name     string
	kind     observability.SpanKind
	attrs    map[string]any
	err      error
	ended    bool
	children []*recordingSpan
}

func (s *recordingSpan) StartChild(ctx context.Context, name string, kind observability.SpanKind) (context.Context, observability.Span) {
	child := &recordingSpan{name: name, kind: kind, attrs: map[string]any{}}
	s.mu.Lock()
	s.children = append(s.children, child)
	s.mu.Unlock()
	return ctx, child
}

func (s *recordingSpan) SetAttribute(key string, value any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attrs == nil {
		s.attrs = map[string]any{}
	}
	s.attrs[key] = value
}

func (s *recordingSpan) SetTokens(_, _, _ int)                 {}
func (s *recordingSpan) SetCost(_ observability.CostBreakdown) {}

func (s *recordingSpan) SetError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *recordingSpan) SetStreamTimings(_, _ float64) {}

func (s *recordingSpan) End() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended = true
}

func (s *recordingSpan) onlyChild(t *testing.T) *recordingSpan {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.children) != 1 {
		t.Fatalf("expected exactly 1 child span, got %d", len(s.children))
	}
	return s.children[0]
}

var _ observability.Span = (*recordingSpan)(nil)

// TestManager_ExecutePlugin_RoutesChildSpanThroughSeam verifies that when the
// request carries a root observability span, the manager opens one child span
// per plugin through Span.StartChild and stamps the plugin outcome — and, on
// failure, the error — via the seam's methods.
func TestManager_ExecutePlugin_RoutesChildSpanThroughSeam(t *testing.T) {
	boom := errors.New("boom")

	tests := []struct {
		name        string
		execFn      func(ctx context.Context, pctx *Context) error
		wantOutcome string
		wantReason  string
		wantErr     bool
	}{
		{
			name:        "ok",
			execFn:      nil,
			wantOutcome: "ok",
		},
		{
			name:        "error",
			execFn:      func(_ context.Context, _ *Context) error { return boom },
			wantOutcome: "error",
			wantErr:     true,
		},
		{
			name: "rejected",
			execFn: func(_ context.Context, pctx *Context) error {
				pctx.Reject = true
				pctx.Reason = "blocked"
				return nil
			},
			wantOutcome: "rejected",
			wantReason:  "blocked",
		},
		{
			name:        "panic",
			execFn:      func(_ context.Context, _ *Context) error { panic("kaboom") },
			wantOutcome: "error",
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange.
			m := NewManager(nil)
			_ = m.Register(StageBeforeRequest, &mockPlugin{
				name:   "probe",
				typ:    TypeGuardrail,
				execFn: tt.execFn,
			})
			root := &recordingSpan{}
			pctx := NewContext(&providers.Request{Model: "gpt-4o"})
			pctx.Span = root

			// Act. The return value is exercised elsewhere; here we assert on the span.
			_ = m.RunBefore(context.Background(), pctx)

			// Assert.
			child := root.onlyChild(t)
			if child.name != "plugin.before_request.probe" {
				t.Errorf("child span name = %q, want %q", child.name, "plugin.before_request.probe")
			}
			if child.kind != observability.SpanKindInternal {
				t.Errorf("child span kind = %v, want SpanKindInternal", child.kind)
			}
			if got := child.attrs[observability.AttrFerroPluginName]; got != "probe" {
				t.Errorf("%s = %v, want probe", observability.AttrFerroPluginName, got)
			}
			if got := child.attrs[observability.AttrFerroPluginKind]; got != string(TypeGuardrail) {
				t.Errorf("%s = %v, want %s", observability.AttrFerroPluginKind, got, TypeGuardrail)
			}
			if got := child.attrs[observability.AttrFerroPluginStage]; got != string(StageBeforeRequest) {
				t.Errorf("%s = %v, want %s", observability.AttrFerroPluginStage, got, StageBeforeRequest)
			}
			if got := child.attrs[observability.AttrFerroPluginOutcome]; got != tt.wantOutcome {
				t.Errorf("%s = %v, want %s", observability.AttrFerroPluginOutcome, got, tt.wantOutcome)
			}
			if tt.wantReason != "" {
				if got := child.attrs[observability.AttrFerroPluginReason]; got != tt.wantReason {
					t.Errorf("%s = %v, want %s", observability.AttrFerroPluginReason, got, tt.wantReason)
				}
			}
			if tt.wantErr && child.err == nil {
				t.Error("expected SetError to be called on the child span")
			}
			if !tt.wantErr && child.err != nil {
				t.Errorf("unexpected SetError(%v) on the child span", child.err)
			}
			if !child.ended {
				t.Error("expected child span End() to be called")
			}
		})
	}
}

// TestManager_ExecutePlugin_NilSpanEmitsNoChild verifies that with no root span
// on the context the plugin still runs and the manager emits no span, staying a
// zero-cost no-op.
func TestManager_ExecutePlugin_NilSpanEmitsNoChild(t *testing.T) {
	m := NewManager(nil)
	called := false
	_ = m.Register(StageBeforeRequest, &mockPlugin{
		name: "probe",
		typ:  TypeGuardrail,
		execFn: func(_ context.Context, _ *Context) error {
			called = true
			return nil
		},
	})

	pctx := NewContext(&providers.Request{Model: "gpt-4o"})
	// pctx.Span is intentionally left nil.
	if err := m.RunBefore(context.Background(), pctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("plugin was not executed on the nil-span path")
	}
}

// TestManager_ConcurrentRegisterAndRunBefore verifies that concurrent calls to
// Register and RunBefore do not race. Requires -race to detect the defect: the
// fix adds a lock to Register but RunBefore ranges over m.before without
// holding m.mu.RLock(), so the goroutine scheduler can interleave a slice
// grow (in Register) with the range read (in RunBefore).
// Each goroutine builds its own Context for the reason spelled out on
// TestManager_ConcurrentRegisterAndRunAfter below: a Context belongs to one
// in-flight request, and the stage runners write the current stage into it.
func TestManager_ConcurrentRegisterAndRunBefore(_ *testing.T) {
	m := NewManager(nil)

	var wg sync.WaitGroup
	const n = 200

	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = m.Register(StageBeforeRequest, &mockPlugin{name: "p", typ: TypeGuardrail})
		}()
		go func() {
			defer wg.Done()
			_ = m.RunBefore(context.Background(), NewContext(&providers.Request{Model: "gpt-4o"}))
		}()
	}
	wg.Wait()
}

// TestManager_ConcurrentRegisterAndRunAfter is the same race check for
// RunAfter / m.after. Each goroutine builds its own Context because a Context
// belongs to a single in-flight request: the stage runners and the plugins they
// call write to it, so sharing one across requests would race on the Context
// rather than on what these tests are aimed at, the manager's plugin slices.
func TestManager_ConcurrentRegisterAndRunAfter(_ *testing.T) {
	m := NewManager(nil)

	var wg sync.WaitGroup
	const n = 200

	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = m.Register(StageAfterRequest, &mockPlugin{name: "p", typ: TypeLogging})
		}()
		go func() {
			defer wg.Done()
			pctx := NewContext(&providers.Request{Model: "gpt-4o"})
			pctx.Response = &providers.Response{ID: "r1"}
			_ = m.RunAfter(context.Background(), pctx)
		}()
	}
	wg.Wait()
}

// TestManager_ConcurrentRegisterAndRunOnError is the same race check for
// RunOnError / m.onErr.
func TestManager_ConcurrentRegisterAndRunOnError(_ *testing.T) {
	m := NewManager(nil)

	var wg sync.WaitGroup
	const n = 200

	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = m.Register(StageOnError, &mockPlugin{name: "p", typ: TypeLogging})
		}()
		go func() {
			defer wg.Done()
			m.RunOnError(context.Background(), NewContext(&providers.Request{Model: "gpt-4o"}))
		}()
	}
	wg.Wait()
}

// TestManager_ConcurrentRegisterAndHasPlugins checks that HasPlugins does not
// race with concurrent Register calls (it reads all three slice lengths without
// a lock).
func TestManager_ConcurrentRegisterAndHasPlugins(_ *testing.T) {
	m := NewManager(nil)

	var wg sync.WaitGroup
	const n = 200

	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = m.Register(StageBeforeRequest, &mockPlugin{name: "p", typ: TypeGuardrail})
		}()
		go func() {
			defer wg.Done()
			_ = m.HasPlugins()
		}()
	}
	wg.Wait()
}

// TestManager_ConcurrentAllReadersAndRegister fires RunBefore, RunAfter,
// RunOnError, and HasPlugins simultaneously against concurrent Register calls.
// This catches any reader–reader or reader–writer races that the individual
// pairwise tests might miss under a specific schedule.
func TestManager_ConcurrentAllReadersAndRegister(_ *testing.T) {
	m := NewManager(nil)

	var wg sync.WaitGroup
	const n = 100

	for i := 0; i < n; i++ {
		wg.Add(5)
		go func() {
			defer wg.Done()
			_ = m.Register(StageBeforeRequest, &mockPlugin{name: "p", typ: TypeGuardrail})
		}()
		go func() {
			defer wg.Done()
			_ = m.RunBefore(context.Background(), NewContext(&providers.Request{Model: "gpt-4o"}))
		}()
		go func() {
			defer wg.Done()
			pctx := NewContext(&providers.Request{Model: "gpt-4o"})
			pctx.Response = &providers.Response{ID: "r1"}
			_ = m.RunAfter(context.Background(), pctx)
		}()
		go func() {
			defer wg.Done()
			m.RunOnError(context.Background(), NewContext(&providers.Request{Model: "gpt-4o"}))
		}()
		go func() {
			defer wg.Done()
			_ = m.HasPlugins()
		}()
	}
	wg.Wait()
}

func TestUnknownPluginTypeFailsClosed(t *testing.T) {
	// A third-party plugin type the gateway has never heard of gates the request
	// by default. Failing open on an unrecognised type would mean a typo in
	// `type:` silently disables a guardrail.
	m := NewManager(nil)
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

func TestRunBefore_ObservabilityPluginErrorsFailOpen(t *testing.T) {
	// A broken log sink or metrics backend must never take down the request
	// path: it observes the request, it does not gate it.
	for _, typ := range []PluginType{TypeLogging, TypeMetrics} {
		t.Run(string(typ), func(t *testing.T) {
			m := NewManager(nil)
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

// TestRunBefore_SkipProviderDoesNotStopTheChain is the guardrail-bypass
// regression. A response-cache hit suppresses the provider call and nothing
// else: the rate limiter and the budget listed behind it in the shipped example
// config must still run, or one prompt repeated indefinitely costs nothing and
// is refused by nobody.
func TestRunBefore_SkipProviderDoesNotStopTheChain(t *testing.T) {
	m := NewManager(nil)
	_ = m.Register(StageBeforeRequest, &mockPlugin{
		name: "cache",
		typ:  TypeTransform,
		execFn: func(_ context.Context, pctx *Context) error {
			pctx.Response = &providers.Response{ID: "cached"}
			pctx.SkipProvider = true
			return nil
		},
	})
	reached := false
	_ = m.Register(StageBeforeRequest, &mockPlugin{
		name: "later",
		typ:  TypeGuardrail,
		execFn: func(context.Context, *Context) error {
			reached = true
			return nil
		},
	})

	pctx := NewContext(&providers.Request{Model: "gpt-4o"})
	if err := m.RunBefore(context.Background(), pctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reached {
		t.Error("SkipProvider suppressed a plugin behind it; it may only suppress the provider call")
	}
	if !pctx.SkipProvider {
		t.Error("SkipProvider must survive RunBefore, or the gateway cannot serve the short-circuit response")
	}
}

// TestRunBefore_RejectionStillRunsTheErrorStage covers the request an operator
// otherwise has no record of. A guardrail denial ends the before-request stage,
// but the on_error stage still runs, so a blocked request reaches the request
// log and the dashboard instead of existing only in stdout.
func TestRunBefore_RejectionStillRunsTheErrorStage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		execFn func(context.Context, *Context) error
	}{
		{
			name: "a guardrail denies",
			execFn: func(_ context.Context, pctx *Context) error {
				pctx.Reject = true
				pctx.Reason = "blocked by content policy"
				return nil
			},
		},
		{
			name: "a fail-closed plugin breaks",
			execFn: func(context.Context, *Context) error {
				return errors.New("guardrail backend unreachable")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(nil)
			_ = m.Register(StageBeforeRequest, &mockPlugin{name: "guard", typ: TypeGuardrail, execFn: tc.execFn})

			var observed error
			var observedStage Stage
			_ = m.Register(StageOnError, &mockPlugin{
				name: "recorder",
				typ:  TypeLogging,
				execFn: func(_ context.Context, pctx *Context) error {
					observed = pctx.Error
					observedStage = pctx.Stage
					return nil
				},
			})

			pctx := NewContext(&providers.Request{Model: "gpt-4o"})
			err := m.RunBefore(context.Background(), pctx)
			if err == nil {
				t.Fatal("expected the request to be aborted")
			}
			if observed == nil {
				t.Fatal("the on_error stage did not run, so the aborted request left no record")
			}
			if observed != err {
				t.Errorf("on_error saw %v, want the abort reason %v", observed, err)
			}
			if observedStage != StageOnError {
				t.Errorf("Stage during on_error = %q, want %q", observedStage, StageOnError)
			}
		})
	}
}

// TestRunAfter_RunsEveryPluginOnASkippedProvider checks the other half: the
// after-request stage runs in full for a request served from cache, and
// SkipProvider is still readable there so a plugin that bills for provider work
// can tell there was none.
func TestRunAfter_RunsEveryPluginOnASkippedProvider(t *testing.T) {
	m := NewManager(nil)
	var ran []string
	var sawSkip []bool
	for _, name := range []string{"first", "second"} {
		_ = m.Register(StageAfterRequest, &mockPlugin{
			name: name,
			typ:  TypeLogging,
			execFn: func(_ context.Context, pctx *Context) error {
				ran = append(ran, name)
				sawSkip = append(sawSkip, pctx.SkipProvider)
				return nil
			},
		})
	}

	pctx := NewContext(&providers.Request{Model: "gpt-4o"})
	pctx.Response = &providers.Response{ID: "cached"}
	pctx.SkipProvider = true // as a before-request cache hit leaves it

	if err := m.RunAfter(context.Background(), pctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ran) != 2 || ran[0] != "first" || ran[1] != "second" {
		t.Errorf("after-request plugins run = %v, want [first second]", ran)
	}
	for i, saw := range sawSkip {
		if !saw {
			t.Errorf("plugin %d could not tell the provider was never called", i)
		}
	}
}

// TestRunStages_StampTheCurrentStage pins the fact plugins branch on. Inferring
// the stage from which fields are nil is what let a cache hit look like a
// completed request to the budget plugin.
func TestRunStages_StampTheCurrentStage(t *testing.T) {
	m := NewManager(nil)
	seen := map[Stage]Stage{}
	for _, stage := range []Stage{StageBeforeRequest, StageAfterRequest, StageOnError} {
		_ = m.Register(stage, &mockPlugin{
			name: string(stage),
			typ:  TypeLogging,
			execFn: func(_ context.Context, pctx *Context) error {
				seen[stage] = pctx.Stage
				return nil
			},
		})
	}

	pctx := NewContext(&providers.Request{Model: "gpt-4o"})
	if err := m.RunBefore(context.Background(), pctx); err != nil {
		t.Fatalf("RunBefore: %v", err)
	}
	if err := m.RunAfter(context.Background(), pctx); err != nil {
		t.Fatalf("RunAfter: %v", err)
	}
	m.RunOnError(context.Background(), pctx)

	for _, stage := range []Stage{StageBeforeRequest, StageAfterRequest, StageOnError} {
		if seen[stage] != stage {
			t.Errorf("plugin at %q saw Stage %q", stage, seen[stage])
		}
	}
}
