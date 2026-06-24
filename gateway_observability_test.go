package aigateway

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/observability"
	"github.com/ferro-labs/ai-gateway/providers"
)

// testModel is the model name used across observability tests.
const testModel = "gpt-4o"

// eventCapturingProvider extends fakeProvider with event-capture and
// EventRecordingProvider support so tests can assert on RecordEvent calls.
type eventCapturingProvider struct {
	fakeProvider
	mu              sync.Mutex
	events          []observability.Event
	eventCtxs       []context.Context
	recordingActive bool
}

func (p *eventCapturingProvider) RecordEvent(ctx context.Context, evt observability.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, evt)
	p.eventCtxs = append(p.eventCtxs, ctx)
}

func (p *eventCapturingProvider) lastEventCtx() context.Context {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.eventCtxs) == 0 {
		return nil
	}
	return p.eventCtxs[len(p.eventCtxs)-1]
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
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock"}},
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
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock"}},
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
	if fp.attrs.RoutingStrategy != string(ModeSingle) {
		t.Errorf("RequestAttrs.RoutingStrategy = %q, want %q", fp.attrs.RoutingStrategy, string(ModeSingle))
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
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock"}},
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
	if fp.attrs.RoutingStrategy != string(ModeSingle) {
		t.Errorf("RequestAttrs.RoutingStrategy = %q, want %q", fp.attrs.RoutingStrategy, string(ModeSingle))
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
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock"}},
	})
	ep := &eventCapturingProvider{recordingActive: true}
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
	for range 200 {
		if evtCtx = ep.lastEventCtx(); evtCtx != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if evtCtx == nil {
		t.Fatal("no observability event was recorded")
	}
	if err := evtCtx.Err(); err != nil {
		t.Fatalf("event ctx should be detached from cancellation, got %v", err)
	}
	if v, _ := evtCtx.Value(marker).(string); v != "trace-xyz" {
		t.Fatalf("event ctx lost request trace value: got %q, want trace-xyz", v)
	}
}

func TestGateway_Route_StampsErrorOnSpan(t *testing.T) {
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock"}},
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
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock"}},
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

	evts := ep.capturedEvents()
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
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock"}},
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

	evts := ep.capturedEvents()
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

// TestGateway_Route_NoEventWhenNoOp asserts that with a NoOp provider
// (obsEventsActive=false) Route does NOT call RecordEvent.
func TestGateway_Route_NoEventWhenNoOp(t *testing.T) {
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock"}},
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
	gw, _ := New(Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: "mock"}},
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
