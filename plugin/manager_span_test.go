package plugin

import (
	"context"
	"errors"
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
			m := NewManager()
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
	m := NewManager()
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
