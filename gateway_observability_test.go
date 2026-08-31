package aigateway

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/observability"
	"github.com/ferro-labs/ai-gateway/pkg/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
	"github.com/ferro-labs/ai-gateway/pkg/metrics"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/prometheus/client_golang/prometheus"
)

// testModel is the model name used across observability tests.
const testModel = "gpt-4o"

// eventCapturingProvider extends fakeProvider with event-capture and
// EventRecordingProvider support so tests can assert on RecordEvent calls.
type eventCapturingProvider struct {
	fakeProvider
	mu              sync.Mutex
	events          []observability.Event
	recorded        chan recordedEvent
	recordingActive bool
}

type recordedEvent struct {
	ctx   context.Context
	event observability.Event
}

func (p *eventCapturingProvider) RecordEvent(ctx context.Context, evt observability.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, evt)
	if p.recorded != nil {
		select {
		case p.recorded <- recordedEvent{ctx: ctx, event: evt}:
		default:
		}
	}
}

func (p *eventCapturingProvider) RecordingEnabled() bool {
	return p.recordingActive
}

func (p *eventCapturingProvider) capturedEvents() []observability.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]observability.Event, len(p.events))
	copy(out, p.events)
	return out
}

func eventsWithSubject(events []observability.Event, subject string) []observability.Event {
	var matched []observability.Event
	for _, event := range events {
		if event.Subject == subject {
			matched = append(matched, event)
		}
	}
	return matched
}

// Compile-time interface guard: eventCapturingProvider must satisfy both
// observability.Provider and observability.EventRecordingProvider.
var (
	_ observability.Provider               = (*eventCapturingProvider)(nil)
	_ observability.EventRecordingProvider = (*eventCapturingProvider)(nil)
)

// fakeProvider records all calls so tests can assert that Gateway.Route
// wires the observability hooks correctly (root span, error stamping,
// token/cost stamping).
type fakeProvider struct {
	mu       sync.Mutex
	attrs    observability.RequestAttrs
	spans    []*fakeSpan
	shutdown bool
}

func (p *fakeProvider) StartRequestSpan(ctx context.Context, attrs observability.RequestAttrs) (context.Context, observability.Span) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.attrs = attrs
	sp := &fakeSpan{}
	p.spans = append(p.spans, sp)
	return ctx, sp
}

func (p *fakeProvider) RecordEvent(_ context.Context, _ observability.Event) {}
func (p *fakeProvider) Shutdown(_ context.Context) error {
	p.shutdown = true
	return nil
}

func (p *fakeProvider) rootSpan() *fakeSpan {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.spans) == 0 {
		return nil
	}
	return p.spans[0]
}

type fakeSpan struct {
	mu         sync.Mutex
	attrs      map[string]any
	tokensIn   int
	tokensOut  int
	cost       observability.CostBreakdown
	err        error
	ended      bool
	streamTTFT float64
	streamTTLT float64
}

func (s *fakeSpan) StartChild(ctx context.Context, _ string, _ observability.SpanKind) (context.Context, observability.Span) {
	return ctx, s
}

func (s *fakeSpan) SetAttribute(key string, value any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attrs == nil {
		s.attrs = map[string]any{}
	}
	s.attrs[key] = value
}

func (s *fakeSpan) SetTokens(in, out, _ int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokensIn = in
	s.tokensOut = out
}

func (s *fakeSpan) SetCost(c observability.CostBreakdown) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cost = c
}

func (s *fakeSpan) SetError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *fakeSpan) SetStreamTimings(ttft, ttlt float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streamTTFT = ttft
	s.streamTTLT = ttlt
}

func (s *fakeSpan) End() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended = true
}

func TestGateway_Route_StartsRootSpan(t *testing.T) {
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "mock"}},
	})

	fp := &fakeProvider{}
	gw.SetObservability(fp)

	gw.RegisterProvider(&mockProvider{
		name:   "mock",
		models: []string{testModel},
		resp: &providers.Response{
			ID:       "r1",
			Provider: "mock",
			Model:    testModel,
			Usage:    providers.Usage{PromptTokens: 10, CompletionTokens: 20},
		},
	})

	_, err := gw.Route(context.Background(), providers.Request{
		Model:    testModel,
		Stream:   false,
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if fp.attrs.Operation != "chat" {
		t.Errorf("Operation = %q, want %q", fp.attrs.Operation, "chat")
	}
	if fp.attrs.RequestModel != testModel {
		t.Errorf("RequestModel = %q, want %q", fp.attrs.RequestModel, testModel)
	}

	sp := fp.rootSpan()
	if sp == nil {
		t.Fatal("expected a root span")
	}
	if !sp.ended {
		t.Error("expected span.End() to be called")
	}
	if sp.tokensIn != 10 || sp.tokensOut != 20 {
		t.Errorf("tokens = (%d, %d), want (10, 20)", sp.tokensIn, sp.tokensOut)
	}
	if got, ok := sp.attrs[observability.AttrGenAIResponseModel]; !ok || got != testModel {
		t.Errorf("response model attr = %v, want gpt-4o", got)
	}
}

func TestGateway_Route_StampsRoutingAttrs(t *testing.T) {
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "mock"}},
	})

	fp := &fakeProvider{}
	gw.SetObservability(fp)

	gw.RegisterProvider(&mockProvider{
		name:   "mock",
		models: []string{testModel},
		resp: &providers.Response{
			ID:       "r1",
			Provider: "mock",
			Model:    testModel,
			Usage:    providers.Usage{PromptTokens: 5, CompletionTokens: 10},
		},
	})

	_, err := gw.Route(context.Background(), providers.Request{
		Model:    testModel,
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// RoutingStrategy should be set at span start.
	if fp.attrs.RoutingStrategy != string(config.ModeSingle) {
		t.Errorf("RequestAttrs.RoutingStrategy = %q, want %q", fp.attrs.RoutingStrategy, string(config.ModeSingle))
	}

	sp := fp.rootSpan()
	if sp == nil {
		t.Fatal("expected a root span")
	}

	// ferro.routing.target_key should be stamped after resolution.
	got, ok := sp.attrs[observability.AttrFerroRoutingTargetKey]
	if !ok {
		t.Fatal("expected ferro.routing.target_key attribute to be set")
	}
	if s, ok := got.(string); !ok || s == "" {
		t.Errorf("ferro.routing.target_key must be a non-empty string, got %T(%v)", got, got)
	}
}

func TestGateway_RouteStream_StampsRoutingAttrs(t *testing.T) {
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "mock"}},
	})

	fp := &fakeProvider{}
	gw.SetObservability(fp)

	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{
			name:   "mock",
			models: []string{testModel},
		},
	})

	ch, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    testModel,
		Stream:   true,
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Drain the stream.
	//nolint:revive // intentionally draining the stream channel to completion
	for range ch {
	}

	// RoutingStrategy should be set at span start.
	if fp.attrs.RoutingStrategy != string(config.ModeSingle) {
		t.Errorf("RequestAttrs.RoutingStrategy = %q, want %q", fp.attrs.RoutingStrategy, string(config.ModeSingle))
	}

	sp := fp.rootSpan()
	if sp == nil {
		t.Fatal("expected a root span")
	}

	// ferro.routing.target_key should be stamped after provider resolution.
	got, ok := sp.attrs[observability.AttrFerroRoutingTargetKey]
	if !ok {
		t.Fatal("expected ferro.routing.target_key attribute to be set on stream span")
	}
	if s, ok := got.(string); !ok || s == "" {
		t.Errorf("ferro.routing.target_key must be a non-empty string, got %T(%v)", got, got)
	}
}

// TestGateway_RouteStream_EventContextDetachedButTraced covers issue #181: the
// observability event recorded from the streamwrap goroutine must be detached
// from request cancellation (so it is not dead-on-arrival once the HTTP handler
// returns) while still carrying the request's trace context / values via
// context.WithoutCancel.
func TestGateway_RouteStream_EventContextDetachedButTraced(t *testing.T) {
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "mock"}},
	})

	ep := &eventCapturingProvider{
		// Every event the request emits before the test drains the channel has to fit:
		// one routing attempt, the stream start, and the terminal event at minimum.
		recorded:        make(chan recordedEvent, 16),
		recordingActive: true,
	}
	gw.SetObservability(ep)

	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "mock", models: []string{testModel}},
	})

	type ctxKey string
	const marker ctxKey = "trace-marker"
	reqCtx, cancel := context.WithCancel(context.WithValue(context.Background(), marker, "trace-xyz"))

	ch, err := gw.RouteStream(reqCtx, providers.Request{
		Model:    testModel,
		Stream:   true,
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("RouteStream: %v", err)
	}
	// Simulate the HTTP handler returning before the streamwrap goroutine
	// records its completion event.
	cancel()
	//nolint:revive // intentionally draining the stream channel to completion
	for range ch {
	}

	var evtCtx context.Context
	for evtCtx == nil {
		select {
		case recorded := <-ep.recorded:
			if recorded.event.Subject == "gateway.request.completed" {
				evtCtx = recorded.ctx
			}
		case <-time.After(time.Second):
			t.Fatal("no completed observability event was recorded")
		}
	}
	if err := evtCtx.Err(); err != nil {
		t.Fatalf("event ctx should be detached from cancellation, got %v", err)
	}
	if v, _ := evtCtx.Value(marker).(string); v != "trace-xyz" {
		t.Fatalf("event ctx lost request trace value: got %q, want trace-xyz", v)
	}
}

func TestGateway_Route_StampsErrorOnSpan(t *testing.T) {
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "mock"}},
	})

	fp := &fakeProvider{}
	gw.SetObservability(fp)

	wantErr := errors.New("provider exploded")
	gw.RegisterProvider(&mockProvider{
		name:   "mock",
		models: []string{testModel},
		err:    wantErr,
	})

	_, err := gw.Route(context.Background(), providers.Request{
		Model:    testModel,
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error from Route")
	}

	sp := fp.rootSpan()
	if sp == nil {
		t.Fatal("expected a root span")
	}
	if sp.err == nil {
		t.Fatal("expected span.SetError to be called")
	}
	if !sp.ended {
		t.Error("expected span.End() to be called even on error path")
	}
}

// ---- EventRecordingProvider / obsEventsActive pathway tests ----

// TestGateway_Route_EmitsCompletedEvent asserts that a successful Route call
// delivers a "gateway.request.completed" Event to the provider's RecordEvent
// when the provider implements EventRecordingProvider with RecordingEnabled()==true.
func TestGateway_Route_EmitsCompletedEvent(t *testing.T) {
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "mock"}},
	})

	ep := &eventCapturingProvider{recordingActive: true}
	gw.SetObservability(ep)

	gw.RegisterProvider(&mockProvider{
		name:   "mock",
		models: []string{testModel},
		resp: &providers.Response{
			ID:       "r1",
			Provider: "mock",
			Model:    testModel,
			Usage:    providers.Usage{PromptTokens: 10, CompletionTokens: 20},
		},
	})

	_, err := gw.Route(context.Background(), providers.Request{
		Model:    testModel,
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	evts := eventsWithSubject(ep.capturedEvents(), "gateway.request.completed")
	if len(evts) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evts))
	}
	evt := evts[0]
	if evt.Subject != "gateway.request.completed" {
		t.Errorf("event Subject = %q, want gateway.request.completed", evt.Subject)
	}
	if evt.Provider != "mock" {
		t.Errorf("event Provider = %q, want mock", evt.Provider)
	}
	if evt.Model != testModel {
		t.Errorf("event Model = %q, want gpt-4o", evt.Model)
	}
	if evt.Status != 200 {
		t.Errorf("event Status = %d, want 200", evt.Status)
	}
	if evt.TokensIn != 10 {
		t.Errorf("event TokensIn = %d, want 10", evt.TokensIn)
	}
	if evt.TokensOut != 20 {
		t.Errorf("event TokensOut = %d, want 20", evt.TokensOut)
	}
}

// TestGateway_Route_EmitsFailedEvent asserts that a failed Route call
// delivers a "gateway.request.failed" Event to RecordEvent.
func TestGateway_Route_EmitsFailedEvent(t *testing.T) {
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "mock"}},
	})

	ep := &eventCapturingProvider{recordingActive: true}
	gw.SetObservability(ep)

	wantErr := errors.New("provider exploded")
	gw.RegisterProvider(&mockProvider{
		name:   "mock",
		models: []string{testModel},
		err:    wantErr,
	})

	_, err := gw.Route(context.Background(), providers.Request{
		Model:    testModel,
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected Route to return an error")
	}

	evts := eventsWithSubject(ep.capturedEvents(), "gateway.request.failed")
	if len(evts) != 1 {
		t.Fatalf("expected 1 event, got %d: %v", len(evts), evts)
	}
	evt := evts[0]
	if evt.Subject != "gateway.request.failed" {
		t.Errorf("event Subject = %q, want gateway.request.failed", evt.Subject)
	}
	if evt.Status != 500 {
		t.Errorf("event Status = %d, want 500", evt.Status)
	}
	if evt.Error == "" {
		t.Error("event Error should be non-empty for failed requests")
	}
}

func TestGateway_Route_RecordsEachRetryAndFailoverAttempt(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets: []config.Target{
			{VirtualKey: "primary", ModelMap: map[string]string{testModel: "upstream-primary"}, Retry: &config.RetryConfig{Attempts: 2, InitialBackoffMs: 1}},
			{VirtualKey: "secondary", ModelMap: map[string]string{testModel: "upstream-secondary"}},
		},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	ep := &eventCapturingProvider{recordingActive: true}
	gw.SetObservability(ep)

	primaryCalls := 0
	primary := providers.WithName(&mockProvider{
		name: "vendor-primary", models: []string{testModel},
		completeFn: func(context.Context, providers.Request) (*providers.Response, error) {
			primaryCalls++
			if primaryCalls == 1 {
				return nil, core.StatusError("vendor-primary", http.StatusServiceUnavailable, "credential AKIAIOSFODNN7EXAMPLE failed")
			}
			return nil, errors.New("dial account@example.com: connection refused")
		},
	}, "primary")
	secondary := providers.WithName(&mockProvider{
		name: "vendor-secondary", models: []string{testModel},
		resp: &providers.Response{ID: "ok", Provider: "secondary", Model: "upstream-secondary"},
	}, "secondary")
	gw.RegisterProvider(primary)
	gw.RegisterProvider(secondary)

	ctx := logger.WithTraceID(context.Background(), "0123456789abcdef0123456789abcdef")
	if _, err := gw.Route(ctx, providers.Request{Model: testModel, Messages: []providers.Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatalf("Route: %v", err)
	}

	attempts := eventsWithSubject(ep.capturedEvents(), observability.SubjectRoutingAttempt)
	if len(attempts) != 3 {
		t.Fatalf("routing attempt events = %d, want 3: %#v", len(attempts), attempts)
	}
	want := []observability.RoutingAttempt{
		{TargetKey: "primary", Provider: "vendor-primary", RoutedModel: testModel, UpstreamModel: "upstream-primary", Sequence: 1, TargetSequence: 1, Status: http.StatusServiceUnavailable, Outcome: observability.RoutingAttemptError, Error: "vendor-primary API error (503): credential [REDACTED_AWS_KEY] failed"},
		{TargetKey: "primary", Provider: "vendor-primary", RoutedModel: testModel, UpstreamModel: "upstream-primary", Sequence: 2, TargetSequence: 2, Status: http.StatusInternalServerError, Outcome: observability.RoutingAttemptError, Error: "dial [REDACTED_EMAIL]: connection refused"},
		{TargetKey: "secondary", Provider: "vendor-secondary", RoutedModel: testModel, UpstreamModel: "upstream-secondary", Sequence: 3, TargetSequence: 1, Status: http.StatusOK, Outcome: observability.RoutingAttemptSuccess},
	}
	for i, event := range attempts {
		if event.TraceID != "0123456789abcdef0123456789abcdef" || event.Timestamp.IsZero() {
			t.Errorf("attempt %d envelope = trace %q timestamp %v", i+1, event.TraceID, event.Timestamp)
		}
		if event.RoutingAttempt == nil {
			t.Fatalf("attempt %d payload is nil", i+1)
		}
		got := *event.RoutingAttempt
		if got.LatencyMs < 0 {
			t.Errorf("attempt %d latency = %d, want non-negative", i+1, got.LatencyMs)
		}
		got.LatencyMs = 0
		if got != want[i] {
			t.Errorf("attempt %d = %#v, want %#v", i+1, got, want[i])
		}
	}
	if terminal := eventsWithSubject(ep.capturedEvents(), "gateway.request.completed"); len(terminal) != 1 {
		t.Errorf("completed terminal events = %d, want 1", len(terminal))
	}
}

func TestGateway_Route_AttributesABVariantAcrossRetriesAndAdvancement(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeABTest, ABVariants: []config.ABVariantConfig{
			{TargetKey: "primary", Weight: 1, Label: "control"},
		}},
		Targets: []config.Target{
			{VirtualKey: "primary", Retry: &config.RetryConfig{Attempts: 2, InitialBackoffMs: 1}},
			{VirtualKey: "secondary"},
		},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	ep := &eventCapturingProvider{recordingActive: true}
	gw.SetObservability(ep)
	gw.RegisterProvider(&mockProvider{name: "primary", models: []string{testModel}, err: errors.New("dial failed")})
	gw.RegisterProvider(&mockProvider{name: "secondary", models: []string{testModel}, resp: &providers.Response{ID: "ok", Provider: "secondary", Model: testModel}})

	if _, err := gw.Route(context.Background(), providers.Request{Model: testModel}); err != nil {
		t.Fatalf("Route: %v", err)
	}
	for _, event := range ep.capturedEvents() {
		if event.Subject != observability.SubjectRoutingAttempt && event.Subject != "gateway.request.completed" {
			continue
		}
		if got := event.Attributes[observability.AttrFerroRoutingABVariantLabel]; got != "control" {
			t.Errorf("%s variant label = %#v, want control", event.Subject, got)
		}
	}
	if attempts := eventsWithSubject(ep.capturedEvents(), observability.SubjectRoutingAttempt); len(attempts) != 3 {
		t.Fatalf("routing attempt events = %d, want 3", len(attempts))
	}
}

func TestGateway_Route_AfterPluginFailureEmitsOneABFailedTerminal(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeABTest, ABVariants: []config.ABVariantConfig{{TargetKey: "mock", Weight: 1, Label: "control"}}},
		Targets:  []config.Target{{VirtualKey: "mock"}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	ep := &eventCapturingProvider{recordingActive: true}
	gw.SetObservability(ep)
	gw.RegisterProvider(&mockProvider{name: "mock", models: []string{testModel}, resp: &providers.Response{Model: "provider-third-id", Provider: "mock"}})

	onErrorCalls := 0
	if err := gw.RegisterPlugin(plugin.StageAfterRequest, &testPlugin{name: "reject-after", typ: plugin.TypeGuardrail, execFn: func(_ context.Context, pctx *plugin.Context) error {
		pctx.Reject = true
		pctx.Reason = "blocked after response"
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	if err := gw.RegisterPlugin(plugin.StageOnError, &testPlugin{name: "count-error", typ: plugin.TypeLogging, execFn: func(context.Context, *plugin.Context) error {
		onErrorCalls++
		return nil
	}}); err != nil {
		t.Fatal(err)
	}

	if _, err := gw.Route(context.Background(), providers.Request{Model: testModel}); err == nil {
		t.Fatal("Route unexpectedly succeeded")
	}
	failed := eventsWithSubject(ep.capturedEvents(), "gateway.request.failed")
	if len(failed) != 1 {
		t.Fatalf("failed terminal events = %d, want 1", len(failed))
	}
	if failed[0].Model != testModel || failed[0].Attributes[observability.AttrFerroRoutingABVariantLabel] != "control" {
		t.Errorf("failed terminal = %#v, want model %q and control label", failed[0], testModel)
	}
	if completed := eventsWithSubject(ep.capturedEvents(), "gateway.request.completed"); len(completed) != 0 {
		t.Errorf("completed terminal events = %d, want 0", len(completed))
	}
	if attempts := eventsWithSubject(ep.capturedEvents(), observability.SubjectRoutingAttempt); len(attempts) != 1 {
		t.Errorf("routing attempt events = %d, want 1", len(attempts))
	}
	if onErrorCalls != 1 {
		t.Errorf("on_error calls = %d, want 1", onErrorCalls)
	}
}

func TestGateway_Route_AttributesConfiguredEmptyABVariant(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeABTest, ABVariants: []config.ABVariantConfig{{TargetKey: "mock", Weight: 1}}},
		Targets:  []config.Target{{VirtualKey: "mock"}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	ep := &eventCapturingProvider{recordingActive: true}
	gw.SetObservability(ep)
	gw.RegisterProvider(&mockProvider{name: "mock", models: []string{testModel}, resp: &providers.Response{ID: "ok", Provider: "mock", Model: testModel}})

	if _, err := gw.Route(context.Background(), providers.Request{Model: testModel}); err != nil {
		t.Fatalf("Route: %v", err)
	}
	for _, event := range ep.capturedEvents() {
		if event.Subject != observability.SubjectRoutingAttempt && event.Subject != "gateway.request.completed" {
			continue
		}
		got, ok := event.Attributes[observability.AttrFerroRoutingABVariantLabel]
		if !ok || got != "" {
			t.Errorf("%s variant label = %#v, present = %v; want present empty string", event.Subject, got, ok)
		}
	}
}

func TestGateway_RouteStream_AttributesABVariantOnTerminalEvents(t *testing.T) {
	for _, tc := range []struct {
		name        string
		streamError error
		wantSubject string
	}{
		{name: "completed", wantSubject: "gateway.request.completed"},
		{name: "failed", streamError: errors.New("stream failed"), wantSubject: "gateway.request.failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gw, err := newTestGateway(t, config.Config{
				Strategy: config.StrategyConfig{Mode: config.ModeABTest, ABVariants: []config.ABVariantConfig{{TargetKey: "mock", Weight: 1, Label: "stream-control"}}},
				Targets:  []config.Target{{VirtualKey: "mock"}},
			})
			if err != nil {
				t.Fatalf("new gateway: %v", err)
			}
			ep := &eventCapturingProvider{recordingActive: true}
			gw.SetObservability(ep)
			gw.RegisterProvider(&mockStreamProvider{mockProvider: mockProvider{name: "mock", models: []string{testModel}}, streamFn: func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
				ch := make(chan providers.StreamChunk, 1)
				if tc.streamError != nil {
					ch <- providers.StreamChunk{Error: tc.streamError}
				}
				close(ch)
				return ch, nil
			}})

			ch, err := gw.RouteStream(context.Background(), providers.Request{Model: testModel, Stream: true})
			if err != nil {
				t.Fatalf("RouteStream: %v", err)
			}
			for chunk := range ch {
				_ = chunk
			}
			terminals := eventsWithSubject(ep.capturedEvents(), tc.wantSubject)
			if len(terminals) != 1 {
				t.Fatalf("%s events = %d, want 1", tc.wantSubject, len(terminals))
			}
			if got := terminals[0].Attributes[observability.AttrFerroRoutingABVariantLabel]; got != "stream-control" {
				t.Errorf("terminal variant label = %#v, want stream-control", got)
			}
		})
	}
}

func TestGateway_RouteStream_AttributesABVariantOnStartFailure(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeABTest, ABVariants: []config.ABVariantConfig{{TargetKey: "mock", Weight: 1, Label: "stream-control"}}},
		Targets:  []config.Target{{VirtualKey: "mock"}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	ep := &eventCapturingProvider{recordingActive: true}
	gw.SetObservability(ep)
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "mock", models: []string{testModel}},
		streamErr:    errors.New("stream start failed"),
	})

	if _, err := gw.RouteStream(context.Background(), providers.Request{Model: testModel, Stream: true}); err == nil {
		t.Fatal("RouteStream unexpectedly succeeded")
	}
	failed := eventsWithSubject(ep.capturedEvents(), "gateway.request.failed")
	if len(failed) != 1 {
		t.Fatalf("failed events = %d, want 1", len(failed))
	}
	if got := failed[0].Attributes[observability.AttrFerroRoutingABVariantLabel]; got != "stream-control" {
		t.Errorf("start failure variant label = %#v, want stream-control", got)
	}
}

type failingRoutedSurfaceProvider struct{ mockProvider }

func (*failingRoutedSurfaceProvider) Embed(context.Context, providers.EmbeddingRequest) (*providers.EmbeddingResponse, error) {
	return nil, errors.New("embedding failed")
}
func (*failingRoutedSurfaceProvider) GenerateImage(context.Context, providers.ImageRequest) (*providers.ImageResponse, error) {
	return nil, errors.New("image failed")
}
func (*failingRoutedSurfaceProvider) Rerank(context.Context, providers.RerankRequest) (*providers.RerankResponse, error) {
	return nil, errors.New("rerank failed")
}
func (*failingRoutedSurfaceProvider) Moderate(context.Context, providers.ModerationRequest) (*providers.ModerationResponse, error) {
	return nil, errors.New("moderation failed")
}
func (*failingRoutedSurfaceProvider) Transcribe(context.Context, providers.TranscriptionRequest) (*providers.TranscriptionResponse, error) {
	return nil, errors.New("transcription failed")
}
func (*failingRoutedSurfaceProvider) Speech(context.Context, providers.SpeechRequest) (*providers.SpeechResponse, error) {
	return nil, errors.New("speech failed")
}

func TestGateway_RoutedSurfaceFailuresAttributeABVariant(t *testing.T) {
	tests := []struct {
		name string
		call func(*Gateway) error
	}{
		{name: "embeddings", call: func(g *Gateway) error {
			_, err := g.Embed(context.Background(), providers.EmbeddingRequest{Model: testModel, Input: "hi"})
			return err
		}},
		{name: "images", call: func(g *Gateway) error {
			_, err := g.GenerateImage(context.Background(), providers.ImageRequest{Model: testModel, Prompt: "hi"})
			return err
		}},
		{name: "rerank", call: func(g *Gateway) error {
			_, err := g.Rerank(context.Background(), providers.RerankRequest{Model: testModel, Query: "hi"})
			return err
		}},
		{name: "moderation", call: func(g *Gateway) error {
			_, err := g.Moderate(context.Background(), providers.ModerationRequest{Model: testModel, Input: "hi"})
			return err
		}},
		{name: "transcription", call: func(g *Gateway) error {
			_, err := g.Transcribe(context.Background(), providers.TranscriptionRequest{Model: testModel})
			return err
		}},
		{name: "speech", call: func(g *Gateway) error {
			_, err := g.Speech(context.Background(), providers.SpeechRequest{Model: testModel, Input: "hi"})
			return err
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gw, err := newTestGateway(t, config.Config{
				Strategy: config.StrategyConfig{Mode: config.ModeABTest, ABVariants: []config.ABVariantConfig{{TargetKey: "mock", Weight: 1, Label: "surface-control"}}},
				Targets:  []config.Target{{VirtualKey: "mock"}},
			})
			if err != nil {
				t.Fatalf("new gateway: %v", err)
			}
			ep := &eventCapturingProvider{recordingActive: true}
			gw.SetObservability(ep)
			gw.RegisterProvider(&failingRoutedSurfaceProvider{mockProvider{name: "mock", models: []string{testModel}}})
			if err := tc.call(gw); err == nil {
				t.Fatal("surface call unexpectedly succeeded")
			}
			failed := eventsWithSubject(ep.capturedEvents(), "gateway.request.failed")
			if len(failed) != 1 {
				t.Fatalf("failed events = %d, want 1", len(failed))
			}
			if got := failed[0].Attributes[observability.AttrFerroRoutingABVariantLabel]; got != "surface-control" {
				t.Errorf("failed variant label = %#v, want surface-control", got)
			}
		})
	}
}

func TestGateway_Route_OmitsABVariantOutsideABTest(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets:  []config.Target{{VirtualKey: "mock"}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	ep := &eventCapturingProvider{recordingActive: true}
	gw.SetObservability(ep)
	gw.RegisterProvider(&mockProvider{name: "mock", models: []string{testModel}, resp: &providers.Response{ID: "ok", Provider: "mock", Model: testModel}})

	if _, err := gw.Route(context.Background(), providers.Request{Model: testModel}); err != nil {
		t.Fatalf("Route: %v", err)
	}
	for _, event := range ep.capturedEvents() {
		if _, ok := event.Attributes[observability.AttrFerroRoutingABVariantLabel]; ok {
			t.Errorf("%s unexpectedly carries an A/B variant label", event.Subject)
		}
	}
}

func TestGateway_Route_RecordsOpenCircuitRefusalAttempt(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets: []config.Target{{
			VirtualKey:     "mock",
			CircuitBreaker: &config.CircuitBreakerConfig{FailureThreshold: 1, Timeout: "1h"},
		}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	ep := &eventCapturingProvider{recordingActive: true}
	gw.SetObservability(ep)

	calls := 0
	gw.RegisterProvider(&mockProvider{
		name: "mock", models: []string{testModel},
		completeFn: func(context.Context, providers.Request) (*providers.Response, error) {
			calls++
			return nil, errors.New("upstream failed")
		},
	})
	req := providers.Request{Model: testModel, Messages: []providers.Message{{Role: "user", Content: "hi"}}}
	if _, err := gw.Route(context.Background(), req); err == nil {
		t.Fatal("seed call should open the circuit")
	}
	before := len(eventsWithSubject(ep.capturedEvents(), observability.SubjectRoutingAttempt))

	_, err = gw.Route(context.Background(), req)
	if !errors.Is(err, circuitbreaker.ErrCircuitOpen) {
		t.Fatalf("refused route error = %v, want ErrCircuitOpen", err)
	}
	if calls != 1 {
		t.Fatalf("provider calls = %d, want 1: refused call must not invoke provider", calls)
	}
	attempts := eventsWithSubject(ep.capturedEvents(), observability.SubjectRoutingAttempt)
	if len(attempts) != before+1 {
		t.Fatalf("refusal attempt event delta = %d, want 1", len(attempts)-before)
	}
	got := attempts[len(attempts)-1].RoutingAttempt
	if got == nil || got.Status != http.StatusServiceUnavailable || got.Outcome != observability.RoutingAttemptError {
		t.Fatalf("refusal attempt = %#v, want status 503 and error outcome", got)
	}
}

func TestGateway_Route_RecordsSaturationRefusalAttempt(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets: []config.Target{{
			VirtualKey:  "mock",
			Concurrency: &config.ConcurrencyConfig{MaxConcurrency: 1, QueueSize: 1},
		}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	ep := &eventCapturingProvider{recordingActive: true}
	gw.SetObservability(ep)

	calls := 0
	gw.RegisterProvider(&mockProvider{
		name: "mock", models: []string{testModel},
		completeFn: func(context.Context, providers.Request) (*providers.Response, error) {
			calls++
			return &providers.Response{ID: "unexpected"}, nil
		},
	})
	lim := gw.limiters["mock"]
	if err := lim.acquire(context.Background()); err != nil {
		t.Fatalf("occupy in-flight slot: %v", err)
	}
	queuedCtx, cancelQueued := context.WithCancel(context.Background())
	queued := make(chan error, 1)
	go func() { queued <- lim.acquire(queuedCtx) }()
	waitFor(t, func() bool { return lim.waiting.Load() == 1 })

	_, routeErr := gw.Route(context.Background(), providers.Request{Model: testModel, Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	cancelQueued()
	if err := <-queued; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued acquire cleanup = %v, want context.Canceled", err)
	}
	lim.release()

	if !errors.Is(routeErr, providers.ErrProviderSaturated) {
		t.Fatalf("refused route error = %v, want ErrProviderSaturated", routeErr)
	}
	if calls != 0 {
		t.Fatalf("provider calls = %d, want 0 for refused call", calls)
	}
	attempts := eventsWithSubject(ep.capturedEvents(), observability.SubjectRoutingAttempt)
	if len(attempts) != 1 {
		t.Fatalf("routing attempt events = %d, want 1", len(attempts))
	}
	got := attempts[0].RoutingAttempt
	if got == nil || got.Status != http.StatusTooManyRequests || got.Outcome != observability.RoutingAttemptError {
		t.Fatalf("refusal attempt = %#v, want status 429 and error outcome", got)
	}
}

// TestGateway_Route_NoEventWhenNoOp asserts that with a NoOp provider
// (obsEventsActive=false) Route does NOT call RecordEvent.
func TestGateway_Route_NoEventWhenNoOp(t *testing.T) {
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "mock"}},
	})

	// Install NoOp — obsEventsActive should stay false.
	gw.SetObservability(observability.NoOp())

	// Sanity-check that obsEventsActive is false.
	gw.mu.RLock()
	active := gw.obsEventsActive
	gw.mu.RUnlock()
	if active {
		t.Error("obsEventsActive should be false for NoOp provider")
	}

	gw.RegisterProvider(&mockProvider{
		name:   "mock",
		models: []string{testModel},
		resp: &providers.Response{
			ID:       "r1",
			Provider: "mock",
			Model:    testModel,
		},
	})

	_, err := gw.Route(context.Background(), providers.Request{
		Model:    testModel,
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No assertion on events — the test just ensures no panic.
}

// TestGateway_SetObservability_SetsObsEventsActive verifies that
// SetObservability correctly caches the RecordingEnabled state.
func TestGateway_SetObservability_SetsObsEventsActive(t *testing.T) {
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "mock"}},
	})

	// Default: NoOp → obsEventsActive == false.
	gw.mu.RLock()
	if gw.obsEventsActive {
		t.Error("expected obsEventsActive=false after default NoOp init")
	}
	gw.mu.RUnlock()

	// Install EventRecordingProvider with RecordingEnabled()==true.
	ep := &eventCapturingProvider{recordingActive: true}
	gw.SetObservability(ep)
	gw.mu.RLock()
	if !gw.obsEventsActive {
		t.Error("expected obsEventsActive=true after EventRecordingProvider with RecordingEnabled()==true")
	}
	gw.mu.RUnlock()

	// Install EventRecordingProvider with RecordingEnabled()==false.
	epOff := &eventCapturingProvider{recordingActive: false}
	gw.SetObservability(epOff)
	gw.mu.RLock()
	if gw.obsEventsActive {
		t.Error("expected obsEventsActive=false after EventRecordingProvider with RecordingEnabled()==false")
	}
	gw.mu.RUnlock()

	// Back to NoOp.
	gw.SetObservability(observability.NoOp())
	gw.mu.RLock()
	if gw.obsEventsActive {
		t.Error("expected obsEventsActive=false after NoOp")
	}
	gw.mu.RUnlock()
}

// newMetricLabelGateway returns a gateway serving exactly "known-model".
func newMetricLabelGateway(t *testing.T) *Gateway {
	t.Helper()
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
	})

	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   mockProviderName,
		models: []string{"known-model"},
		resp:   &providers.Response{ID: "should-not-reach"},
	})
	return gw
}

func TestGateway_MetricModel(t *testing.T) {
	gw := newMetricLabelGateway(t)

	tests := []struct {
		name  string
		model string
		want  string
	}{
		{name: "known model passes through", model: "known-model", want: "known-model"},
		{name: "empty model buckets", model: "", want: metrics.UnknownModelLabel},
		{name: "arbitrary model buckets", model: "totally-made-up-model", want: metrics.UnknownModelLabel},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := gw.metricModel(tt.model); got != tt.want {
				t.Fatalf("metricModel(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

// A plugin rejection carries the raw client model to the "rejected" counter.
//
// The target is a wildcard provider because that is now the only way a plugin
// ever sees an arbitrary model: a model no target serves is refused before the
// before_request stage runs (admitModel), so it reaches the "error" counter —
// asserted by TestGateway_Route_UnroutableModelBucketsErrorMetricLabel below —
// and never the "rejected" one. A provider declaring core.AnyModelProvider
// admits the raw string, which leaves the rejection path exposed to exactly the
// cardinality this bounds.
func TestGateway_Route_PluginRejectsUnknownModelBucketsMetricLabel(t *testing.T) {
	const rawModel = "user-supplied-high-cardinality-rejected"
	if requestMetricLabelExists(t, "", rawModel, "rejected") {
		t.Fatalf("raw model label %q already exists before test", rawModel)
	}

	gw := newMetricLabelGateway(t)
	gw.RegisterProvider(&wildcardProvider{mockProvider: mockProvider{
		name:   mockProviderName,
		models: []string{"known-model"},
		resp:   &providers.Response{ID: "should-not-reach"},
	}})
	_ = gw.RegisterPlugin(plugin.StageBeforeRequest, &testPlugin{
		name: "blocker",
		typ:  plugin.TypeGuardrail,
		execFn: func(_ context.Context, pctx *plugin.Context) error {
			pctx.Reject = true
			pctx.Reason = "blocked"
			return nil
		},
	})

	unknownCounter := metrics.ForRequest("", metrics.UnknownModelLabel).Rejected
	before := counterValue(t, unknownCounter)
	_, err := gw.Route(context.Background(), providers.Request{
		Model:    rawModel,
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected rejection error")
	}
	if delta := counterValue(t, unknownCounter) - before; delta != 1 {
		t.Fatalf("unknown rejected counter delta = %v, want 1", delta)
	}
	if requestMetricLabelExists(t, "", rawModel, "rejected") {
		t.Fatalf("raw rejected model label %q should not be created", rawModel)
	}
}

// The error path needs no plugin at all: any unroutable model reaches it, which
// makes it the cardinality vector a client can trigger against a stock gateway.
func TestGateway_Route_UnroutableModelBucketsErrorMetricLabel(t *testing.T) {
	const rawModel = "user-supplied-high-cardinality-error"
	if requestMetricLabelExists(t, "", rawModel, "error") {
		t.Fatalf("raw model label %q already exists before test", rawModel)
	}

	gw := newMetricLabelGateway(t)
	unknownCounter := metrics.ForRequest("", metrics.UnknownModelLabel).Error
	before := counterValue(t, unknownCounter)

	_, err := gw.Route(context.Background(), providers.Request{
		Model:    rawModel,
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected routing error for unsupported model")
	}
	if delta := counterValue(t, unknownCounter) - before; delta != 1 {
		t.Fatalf("unknown error counter delta = %v, want 1", delta)
	}
	if requestMetricLabelExists(t, "", rawModel, "error") {
		t.Fatalf("raw error model label %q should not be created", rawModel)
	}
}

// wildcardStreamProvider mirrors the providers that declare
// core.AnyModelProvider (ollama, databricks, azure-foundry, …). They route a raw
// client model all the way to a provider call, so a CompleteStream failure lands
// on the error counter with that raw value.
type wildcardStreamProvider struct {
	mockStreamProvider
}

func (p *wildcardStreamProvider) SupportsModel(string) bool { return true }
func (p *wildcardStreamProvider) ServesAnyModel()           {}

// A wildcard provider can accept an arbitrary model and stream successfully, so
// the success/token/duration labels emitted by streamwrap must be bounded too —
// not just the error label. The raw model still reaches cost lookup and events.
func TestGateway_RouteStream_SuccessBucketsMetricLabel(t *testing.T) {
	const rawModel = "user-supplied-high-cardinality-stream-success"
	if requestMetricLabelExists(t, mockProviderName, rawModel, "success") {
		t.Fatalf("raw model label %q already exists before test", rawModel)
	}

	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
	})

	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}
	gw.RegisterProvider(&wildcardStreamProvider{mockStreamProvider: mockStreamProvider{
		mockProvider: mockProvider{name: mockProviderName, models: []string{"known-model"}},
	}})

	unknownCounter := metrics.ForRequest(mockProviderName, metrics.UnknownModelLabel).Success
	before := counterValue(t, unknownCounter)

	ch, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    rawModel,
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("RouteStream: %v", err)
	}
	drainStream(t, ch) // streamwrap emits metrics before closing the channel

	if delta := counterValue(t, unknownCounter) - before; delta != 1 {
		t.Fatalf("unknown success counter delta = %v, want 1", delta)
	}
	if requestMetricLabelExists(t, mockProviderName, rawModel, "success") {
		t.Fatalf("raw success model label %q should not be created", rawModel)
	}
}

func TestGateway_RouteStream_WildcardProviderBucketsErrorMetricLabel(t *testing.T) {
	const rawModel = "user-supplied-high-cardinality-stream"
	if requestMetricLabelExists(t, mockProviderName, rawModel, "error") {
		t.Fatalf("raw model label %q already exists before test", rawModel)
	}

	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
	})

	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}
	gw.RegisterProvider(&wildcardStreamProvider{mockStreamProvider: mockStreamProvider{
		mockProvider: mockProvider{name: mockProviderName, models: []string{"known-model"}},
		streamErr:    errors.New("upstream rejected model"),
	}})

	unknownCounter := metrics.ForRequest(mockProviderName, metrics.UnknownModelLabel).Error
	before := counterValue(t, unknownCounter)

	if _, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    rawModel,
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	}); err == nil {
		t.Fatal("expected streaming error")
	}
	if delta := counterValue(t, unknownCounter) - before; delta != 1 {
		t.Fatalf("unknown stream error counter delta = %v, want 1", delta)
	}
	if requestMetricLabelExists(t, mockProviderName, rawModel, "error") {
		t.Fatalf("raw stream error model label %q should not be created", rawModel)
	}
}

// isHexTraceID reports whether id is the one form every consumer of a trace ID
// accepts — 32 lowercase hex characters, not all zero — which is also what
// internal/otel's IDGenerator requires before it will adopt the logging trace ID
// as the OTel trace_id.
func isHexTraceID(id string) bool {
	if len(id) != 32 {
		return false
	}
	allZero := true
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
		if c != '0' {
			allZero = false
		}
	}
	return !allZero
}

// traceProbe is a before_request plugin that records the trace ID the request
// is carrying by the time plugins run. before_request is used because it is the
// one stage all four routed surfaces reach on the success path.
func traceProbe(seen *string) *testPlugin {
	return &testPlugin{
		name: "trace-probe",
		typ:  plugin.TypeLogging,
		execFn: func(ctx context.Context, _ *plugin.Context) error {
			*seen = logger.TraceIDFromContext(ctx)
			return nil
		},
	}
}

// Every exported routed entry point must seed its own trace ID.
//
// Route, RouteStream, Embed and GenerateImage are all exported, and an embedder
// calls them directly with no HTTP middleware above them — the only place a
// trace ID was ever seeded. Each of them then read
// logger.TraceIDFromContext(ctx) straight into the request span, the request-log
// row and the completed/failed lifecycle event, so for a library caller the
// whole record of every request carried an empty trace ID and nothing about one
// request could be joined to anything else about it.
func TestRoutedSurfaces_SeedTraceIDWithoutHTTPMiddleware(t *testing.T) {
	surfaces := []struct {
		name  string
		model string
		// register installs the provider that serves this surface.
		register func(gw *Gateway)
		call     func(gw *Gateway) error
	}{
		{
			name:  "Route",
			model: "gpt-4o",
			register: func(gw *Gateway) {
				gw.RegisterProvider(&mockProvider{
					name:   mockProviderName,
					models: []string{"gpt-4o"},
					resp:   &providers.Response{ID: "r1", Provider: mockProviderName, Model: "gpt-4o"},
				})
			},
			call: func(gw *Gateway) error {
				_, err := gw.Route(context.Background(), providers.Request{
					Model:    "gpt-4o",
					Messages: []providers.Message{{Role: "user", Content: "hi"}},
				})
				return err
			},
		},
		{
			name:  "RouteStream",
			model: "gpt-4o",
			register: func(gw *Gateway) {
				gw.RegisterProvider(&mockStreamProvider{
					mockProvider: mockProvider{name: mockProviderName, models: []string{"gpt-4o"}},
				})
			},
			call: func(gw *Gateway) error {
				ch, err := gw.RouteStream(context.Background(), providers.Request{
					Model:    "gpt-4o",
					Stream:   true,
					Messages: []providers.Message{{Role: "user", Content: "hi"}},
				})
				if err != nil {
					return err
				}
				for range ch { //nolint:revive // drain so the meter finishes
				}
				return nil
			},
		},
		{
			name:  "Embed",
			model: "text-embedding-3-small",
			register: func(gw *Gateway) {
				gw.RegisterProvider(&mockEmbeddingProvider{
					mockProvider: mockProvider{name: mockProviderName, models: []string{"text-embedding-3-small"}},
					promptTokens: 7,
				})
			},
			call: func(gw *Gateway) error {
				_, err := gw.Embed(context.Background(), providers.EmbeddingRequest{
					Model: "text-embedding-3-small",
					Input: "hi",
				})
				return err
			},
		},
		{
			name:  "GenerateImage",
			model: "dall-e-3",
			register: func(gw *Gateway) {
				gw.RegisterProvider(&mockImageProvider{
					mockProvider: mockProvider{name: mockProviderName, models: []string{"dall-e-3"}},
				})
			},
			call: func(gw *Gateway) error {
				_, err := gw.GenerateImage(context.Background(), providers.ImageRequest{
					Model:  "dall-e-3",
					Prompt: "a cat",
				})
				return err
			},
		},
	}

	for _, s := range surfaces {
		t.Run(s.name, func(t *testing.T) {
			gw, err := newTestGateway(t, config.Config{
				Strategy: config.StrategyConfig{Mode: config.ModeSingle},
				Targets:  []config.Target{{VirtualKey: mockProviderName}},
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			s.register(gw)

			var seen string
			if err := gw.RegisterPlugin(plugin.StageBeforeRequest, traceProbe(&seen)); err != nil {
				t.Fatalf("RegisterPlugin: %v", err)
			}
			if err := s.call(gw); err != nil {
				t.Fatalf("%s: %v", s.name, err)
			}

			if seen == "" {
				t.Fatalf("%s recorded an empty trace ID: a library caller's spans, request-log rows "+
					"and lifecycle events cannot be correlated with each other", s.name)
			}
			if !isHexTraceID(seen) {
				t.Errorf("%s seeded %q, which internal/otel would refuse — the OTel trace_id would differ", s.name, seen)
			}
		})
	}
}

// A caller-supplied trace ID still wins, on every surface: the HTTP layer seeds
// one, and seeding a second here would break the very unification this fixes.
func TestRoute_KeepsACanonicalTraceIDItWasGiven(t *testing.T) {
	const given = "4bf92f3577b34da6a3ce929d0e0e4736"

	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   mockProviderName,
		models: []string{"gpt-4o"},
		resp:   &providers.Response{ID: "r1", Provider: mockProviderName, Model: "gpt-4o"},
	})

	var seen string
	if err := gw.RegisterPlugin(plugin.StageBeforeRequest, traceProbe(&seen)); err != nil {
		t.Fatalf("RegisterPlugin: %v", err)
	}

	ctx := logger.WithTraceID(context.Background(), given)
	if _, err := gw.Route(ctx, providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("Route: %v", err)
	}
	if seen != given {
		t.Errorf("trace ID = %q, want the one the caller supplied %q", seen, given)
	}
}

// providerLabelledFamilies are the metric families keyed by a "provider" label.
// A request that never reached a provider touches several of them, so asserting
// on one counter would leave the rest free to mint an empty label.
var providerLabelledFamilies = []string{
	"gateway_requests_total",
	"gateway_request_duration_seconds",
	"gateway_request_cost_usd_total",
	"gateway_tokens_input_total",
	"gateway_tokens_output_total",
	"gateway_provider_errors_total",
}

// emptyProviderSeries returns the provider-labelled families that currently hold
// a series whose provider label is the empty string.
func emptyProviderSeries(t *testing.T) []string {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	var found []string
	for _, mf := range mfs {
		name := mf.GetName()
		if !containsString(providerLabelledFamilies, name) {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, label := range m.GetLabel() {
				if label.GetName() == "provider" && label.GetValue() == "" {
					found = append(found, name)
				}
			}
		}
	}
	return found
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// TestGateway_NoProviderLabelIsNeverEmpty covers every routed surface reaching a
// verdict before any provider was chosen.
//
// Such a request has no provider to name, and it used to say so with an empty
// label. That is dropped by the `provider=~".+"` matcher dashboards and alerts
// are written with, so the requests disappeared from the panels built to watch
// them, and a blank label reads as one a bug forgot to set. It is recorded as
// metrics.NoProviderLabel instead — the request is still counted, and the label
// says "deliberately none" in a form that selects.
func TestGateway_NoProviderLabelIsNeverEmpty(t *testing.T) {
	if leaked := emptyProviderSeries(t); len(leaked) > 0 {
		t.Fatalf("empty provider label already present before test in %v", leaked)
	}

	denyPlugin := func() plugin.Plugin {
		return &testPlugin{
			name: "deny-all",
			typ:  plugin.TypeGuardrail,
			execFn: func(_ context.Context, pctx *plugin.Context) error {
				pctx.Reject = true
				pctx.Reason = "blocked"
				return nil
			},
		}
	}

	tests := []struct {
		name    string
		deny    bool
		model   string
		surface func(*testing.T, *Gateway, string)
	}{
		{
			name:  "chat denied by a plugin",
			deny:  true,
			model: "known-model",
			surface: func(t *testing.T, gw *Gateway, model string) {
				if _, err := gw.Route(context.Background(), chatRequest(model)); err == nil {
					t.Fatal("expected a plugin rejection")
				}
			},
		},
		{
			name:  "chat naming a model no target serves",
			model: "nobody-serves-this",
			surface: func(t *testing.T, gw *Gateway, model string) {
				if _, err := gw.Route(context.Background(), chatRequest(model)); err == nil {
					t.Fatal("expected a routing error")
				}
			},
		},
		{
			name:  "stream denied by a plugin",
			deny:  true,
			model: "known-model",
			surface: func(t *testing.T, gw *Gateway, model string) {
				if _, err := gw.RouteStream(context.Background(), chatRequest(model)); err == nil {
					t.Fatal("expected a plugin rejection")
				}
			},
		},
		{
			name:  "stream naming a model no target serves",
			model: "nobody-serves-this",
			surface: func(t *testing.T, gw *Gateway, model string) {
				if _, err := gw.RouteStream(context.Background(), chatRequest(model)); err == nil {
					t.Fatal("expected a routing error")
				}
			},
		},
		{
			name:  "embeddings naming a model no target serves",
			model: "nobody-serves-this",
			surface: func(t *testing.T, gw *Gateway, model string) {
				if _, err := gw.Embed(context.Background(), providers.EmbeddingRequest{
					Model: model,
					Input: []string{"hi"},
				}); err == nil {
					t.Fatal("expected a routing error")
				}
			},
		},
		{
			name:  "image generation naming a model no target serves",
			model: "nobody-serves-this",
			surface: func(t *testing.T, gw *Gateway, model string) {
				if _, err := gw.GenerateImage(context.Background(), providers.ImageRequest{
					Model:  model,
					Prompt: "a cat",
				}); err == nil {
					t.Fatal("expected a routing error")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gw, err := newTestGateway(t, config.Config{
				Strategy: config.StrategyConfig{Mode: config.ModeSingle},
				Targets:  []config.Target{{VirtualKey: mockProviderName}},
			})
			if err != nil {
				t.Fatalf("New gateway: %v", err)
			}
			gw.RegisterProvider(&mockStreamProvider{mockProvider: mockProvider{
				name:   mockProviderName,
				models: []string{"known-model"},
				resp:   &providers.Response{ID: "should-not-reach"},
			}})
			if tt.deny {
				if err := gw.RegisterPlugin(plugin.StageBeforeRequest, denyPlugin()); err != nil {
					t.Fatalf("RegisterPlugin: %v", err)
				}
			}

			tt.surface(t, gw, tt.model)

			if leaked := emptyProviderSeries(t); len(leaked) > 0 {
				t.Fatalf("provider=\"\" series minted in %v; a request that reached no provider must record metrics.NoProviderLabel", leaked)
			}
		})
	}
}

func chatRequest(model string) providers.Request {
	return providers.Request{
		Model:    model,
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	}
}
