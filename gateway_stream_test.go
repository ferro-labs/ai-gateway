package aigateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/observability"
	"github.com/ferro-labs/ai-gateway/pkg/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/pkg/metrics"
	"github.com/ferro-labs/ai-gateway/plugin"
	cacheplugin "github.com/ferro-labs/ai-gateway/plugin/cache"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// Streaming on the shared request pipeline.
//
// These tests assert the properties streaming INHERITS by being routed through
// routeTargets rather than through a loop of its own, plus the one property the
// pipeline must NOT take over: a stream's outcome is not known when the start
// call returns, so the circuit breaker and the concurrency slot stay with the
// response. See targetPlan.responseOutlivesCall.

const streamPipelineModel = "stream-pipeline-model-v1"

func streamPipelineRequest() providers.Request {
	return providers.Request{
		Model:    streamPipelineModel,
		Stream:   true,
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	}
}

// countingStreamProvider records how many times CompleteStream was called.
type countingStreamProvider struct {
	mockStreamProvider
	calls atomic.Int64
}

func newCountingStreamProvider(name string, fn func(context.Context) (<-chan providers.StreamChunk, error)) *countingStreamProvider {
	p := &countingStreamProvider{}
	p.name = name
	p.models = []string{streamPipelineModel}
	p.streamFn = func(ctx context.Context, _ providers.Request) (<-chan providers.StreamChunk, error) {
		p.calls.Add(1)
		return fn(ctx)
	}
	return p
}

// chunkStream returns a finished stream carrying one content delta and a usage
// block, which is the shape every OpenAI-compatible provider ends on.
func chunkStream(model string) <-chan providers.StreamChunk {
	ch := make(chan providers.StreamChunk, 2)
	ch <- providers.StreamChunk{
		ID:      "chatcmpl-pipeline",
		Object:  "chat.completion.chunk",
		Created: 1,
		Model:   model,
		Choices: []providers.StreamChoice{{
			Index: 0,
			Delta: providers.MessageDelta{Role: "assistant", Content: "hello"},
		}},
	}
	ch <- providers.StreamChunk{
		Object: "chat.completion.chunk",
		Model:  model,
		Usage:  &providers.Usage{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10},
	}
	close(ch)
	return ch
}

// TestRouteStream_RetryIsHonouredInEveryStrategyMode is the streaming half of
// F19.
//
// Streaming used to run its own target loop, so uniform retry was a property it
// happened to share rather than one it inherited. Routing the start through the
// pipeline makes retryPolicyFor the only retry authority on this surface too,
// which is what stops the two loops drifting apart again.
func TestRouteStream_RetryIsHonouredInEveryStrategyMode(t *testing.T) {
	modes := []config.StrategyMode{
		config.ModeSingle,
		config.ModeFallback,
		config.ModeLoadBalance,
		config.ModeLatency,
		config.ModeCostOptimized,
		config.ModeConditional,
		config.ModeContentBased,
		config.ModeABTest,
	}

	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			cfg := config.Config{
				Strategy: config.StrategyConfig{Mode: mode},
				Targets: []config.Target{{
					VirtualKey: mockProviderName,
					Weight:     1,
					Retry:      &config.RetryConfig{Attempts: 3, InitialBackoffMs: 1},
				}},
			}
			switch mode {
			case config.ModeConditional:
				cfg.Strategy.Conditions = []config.Condition{
					{Key: "model", Value: streamPipelineModel, TargetKey: mockProviderName},
				}
			case config.ModeContentBased:
				cfg.Strategy.ContentConditions = []config.ContentCondition{
					{Type: "prompt_contains", Value: "hi", TargetKey: mockProviderName},
				}
			case config.ModeABTest:
				cfg.Strategy.ABVariants = []config.ABVariantConfig{
					{TargetKey: mockProviderName, Weight: 1, Label: "control"},
				}
			}

			gw, err := newTestGateway(t, cfg)
			if err != nil {
				t.Fatalf("new gateway: %v", err)
			}
			p := newCountingStreamProvider(mockProviderName, func(context.Context) (<-chan providers.StreamChunk, error) {
				return nil, core.StatusError(mockProviderName, http.StatusInternalServerError, "boom")
			})
			gw.RegisterProvider(p)

			if _, err := gw.RouteStream(context.Background(), streamPipelineRequest()); err == nil {
				t.Fatal("expected the stream start to fail")
			}
			if got := p.calls.Load(); got != 3 {
				t.Errorf("mode %s made %d stream-start attempts, want 3", mode, got)
			}
		})
	}
}

// TestRouteStream_FailedStartObservesDuration is F63 on the streaming surface.
//
// A stream that never started still took time. Observing successes only left
// the streaming failure modes out of the histogram entirely, so its quantiles
// answered "how fast are the streams that worked".
func TestRouteStream_FailedStartObservesDuration(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: mockProviderName, models: []string{streamPipelineModel}},
		streamErr:    core.StatusError(mockProviderName, http.StatusInternalServerError, "boom"),
	})

	labels := map[string]string{"provider": mockProviderName, "model": streamPipelineModel}
	before := durationSampleCount(t, labels)
	if _, err := gw.RouteStream(context.Background(), streamPipelineRequest()); err == nil {
		t.Fatal("expected the stream start to fail")
	}
	if delta := durationSampleCount(t, labels) - before; delta != 1 {
		t.Errorf("a failed stream start added %d duration samples, want 1", delta)
	}
	// F64: the failure must name the target that produced it.
	if !requestMetricLabelExists(t, mockProviderName, streamPipelineModel, "error") {
		t.Errorf("no streaming error sample carries provider=%q", mockProviderName)
	}
}

// TestRouteStream_MidStreamFailureObservesDuration is F63 for the failure mode
// that costs the most time: the stream started, ran, and then broke. It is the
// one a duration histogram most needs to see and the one it could not.
func TestRouteStream_MidStreamFailureObservesDuration(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: mockProviderName, models: []string{streamPipelineModel}},
		streamFn: func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
			ch := make(chan providers.StreamChunk, 2)
			ch <- providers.StreamChunk{
				Model:   streamPipelineModel,
				Choices: []providers.StreamChoice{{Delta: providers.MessageDelta{Content: "par"}}},
			}
			ch <- providers.StreamChunk{Error: errors.New("upstream dropped the connection")}
			close(ch)
			return ch, nil
		},
	})

	labels := map[string]string{"provider": mockProviderName, "model": streamPipelineModel}
	before := durationSampleCount(t, labels)
	ch, err := gw.RouteStream(context.Background(), streamPipelineRequest())
	if err != nil {
		t.Fatalf("RouteStream: %v", err)
	}
	for range ch { //nolint:revive // draining is the point
	}
	if delta := durationSampleCount(t, labels) - before; delta != 1 {
		t.Errorf("a mid-stream failure added %d duration samples, want 1", delta)
	}
}

// TestRouteStream_PluginRejectionObservesDuration is F63 on the streaming
// plugin-abort path — the one the non-streaming path already observed.
func TestRouteStream_PluginRejectionObservesDuration(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: mockProviderName, models: []string{streamPipelineModel}},
	})
	if err := gw.RegisterPlugin(plugin.StageBeforeRequest, &testPlugin{
		name: "denier",
		typ:  plugin.TypeGuardrail,
		execFn: func(_ context.Context, pctx *plugin.Context) error {
			pctx.Reject = true
			pctx.Reason = "denied"
			return nil
		},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	// A rejection happens before any target is resolved, so it is recorded
	// against metrics.NoProviderLabel, exactly as the chat path records it.
	labels := map[string]string{"provider": metrics.NoProviderLabel, "model": streamPipelineModel}
	before := durationSampleCount(t, labels)
	if _, err := gw.RouteStream(context.Background(), streamPipelineRequest()); err == nil {
		t.Fatal("expected the guardrail to reject the stream")
	}
	if delta := durationSampleCount(t, labels) - before; delta != 1 {
		t.Errorf("a rejected stream added %d duration samples, want 1", delta)
	}
}

// TestRouteStream_SpanCarriesResponseModel is F71.
//
// gen_ai.response.model is stamped when the channel drains, alongside the
// stream's terminal usage and cost attribution.
func TestRouteStream_SpanCarriesResponseModel(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	fp := &fakeProvider{}
	gw.SetObservability(fp)
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: mockProviderName, models: []string{streamPipelineModel}},
		streamFn: func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
			// Provider chunk identity remains part of the forwarded wire stream;
			// internal terminal attribution remains routed/client-visible.
			return chunkStream(streamPipelineModel + "-2026-01-01"), nil
		},
	})

	ch, err := gw.RouteStream(context.Background(), streamPipelineRequest())
	if err != nil {
		t.Fatalf("RouteStream: %v", err)
	}
	drainStream(t, ch)

	span := fp.rootSpan()
	if span == nil {
		t.Fatal("no root span was started")
	}
	span.mu.Lock()
	got, ok := span.attrs[observability.AttrGenAIResponseModel]
	tokensIn, tokensOut := span.tokensIn, span.tokensOut
	span.mu.Unlock()

	if !ok {
		t.Fatalf("streaming span carries no %s", observability.AttrGenAIResponseModel)
	}
	if want := streamPipelineModel; got != want {
		t.Errorf("%s = %v, want %q", observability.AttrGenAIResponseModel, got, want)
	}
	// The usage half of F71, guarded so a regression cannot zero it again.
	if tokensIn != 7 || tokensOut != 3 {
		t.Errorf("streaming span usage = (%d, %d), want (7, 3)", tokensIn, tokensOut)
	}
}

// TestRouteStream_HalfOpenProbeReleasedOnAbandonedStart is the invariant that
// wedges a target permanently when it breaks.
//
// A start that overruns the gateway's own start deadline is abandoned, but the
// call keeps running and may still SUCCEED. Nothing downstream will ever see
// that channel, so the half-open probe cbProvider admitted is resolved by the
// abandoning goroutine instead. Left held, it is never repaired: resolveState
// only moves Open to HalfOpen on a timer, never a HalfOpen circuit stuck at its
// probe cap, so the target rejects every later request until the process
// restarts.
//
// The pipeline must therefore hand streaming a *cbProvider-decorated provider
// and resolve nothing itself — if the breaker were resolved when the start call
// returned, or if the decoration stopped being outermost, this test wedges.
func TestRouteStream_HalfOpenProbeReleasedOnAbandonedStart(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)

	gw, err := newTestGateway(t, config.Config{
		// Shorter than the start the provider is about to perform, so the wait
		// is abandoned while the call is still in flight.
		RequestTimeout: "50ms",
		Strategy:       config.StrategyConfig{Mode: config.ModeSingle},
		Targets: []config.Target{{
			VirtualKey: mockProviderName,
			CircuitBreaker: &config.CircuitBreakerConfig{
				FailureThreshold: 1,
				SuccessThreshold: 1,
				MaxHalfThreshold: 1,
				Timeout:          "10ms",
			},
		}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: mockProviderName, models: []string{streamPipelineModel}},
		streamFn: func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
			select {
			case started <- struct{}{}:
			default:
			}
			<-release
			return chunkStream(streamPipelineModel), nil
		},
	})

	// Force the breaker open, then let it age into half-open so the next start
	// is admitted as its single probe.
	cb := gw.circuitBreakerFor(t, mockProviderName)
	cb.RecordFailure()
	time.Sleep(30 * time.Millisecond)

	// The start overruns the 50ms deadline and is abandoned.
	if _, err := gw.RouteStream(context.Background(), streamPipelineRequest()); err == nil {
		t.Fatal("expected the abandoned stream start to fail")
	}
	<-started

	// Now let the abandoned call succeed. Its channel reaches nobody, so the
	// probe must be released by the abandoning goroutine.
	close(release)

	deadline := time.Now().Add(2 * time.Second)
	for !cb.Allow() {
		if time.Now().After(deadline) {
			t.Fatal("the circuit never admitted another request: the half-open probe from an abandoned-but-successful start was stranded, wedging the target for good")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestRouteStream_ConcurrencySlotHeldForWholeStream guards the second half of
// responseOutlivesCall.
//
// The pipeline releases a target's concurrency slot when the leaf call returns.
// For a stream that is the moment the response HEADERS arrive, so releasing
// there would let unlimited streams run against a cap that only ever counted
// their setup. Streaming takes the limiter as a provider wrapper instead, which
// holds the slot until the last chunk.
func TestRouteStream_ConcurrencySlotHeldForWholeStream(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets: []config.Target{{
			VirtualKey:  mockProviderName,
			Concurrency: &config.ConcurrencyConfig{MaxConcurrency: 1, QueueSize: 1},
		}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}

	hold := make(chan struct{})
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: mockProviderName, models: []string{streamPipelineModel}},
		streamFn: func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
			ch := make(chan providers.StreamChunk, 1)
			go func() {
				defer close(ch)
				<-hold
				ch <- providers.StreamChunk{
					Model: streamPipelineModel,
					Usage: &providers.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
				}
			}()
			return ch, nil
		},
	})

	// The first stream has started but not finished, so it still owns the only
	// slot.
	first, err := gw.RouteStream(context.Background(), streamPipelineRequest())
	if err != nil {
		t.Fatalf("first RouteStream: %v", err)
	}

	// A second start must find the limiter occupied. Its own context bounds the
	// wait so a released slot shows up as a success rather than a hang.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, secondErr := gw.RouteStream(ctx, streamPipelineRequest())

	close(hold)
	drainStream(t, first)

	if secondErr == nil {
		t.Fatal("a second stream started while the first still held the only concurrency slot: the slot was released when the start call returned instead of when the stream ended")
	}
	if !errors.Is(secondErr, providers.ErrProviderSaturated) && !errors.Is(secondErr, context.DeadlineExceeded) {
		t.Errorf("second stream failed with %v, want saturation or the queue wait timing out", secondErr)
	}
}

// circuitBreakerFor returns the breaker the gateway built for one target,
// creating the per-target resilience state the same way a request would.
func (g *Gateway) circuitBreakerFor(t *testing.T, key string) *circuitbreaker.CircuitBreaker {
	t.Helper()
	g.mu.Lock()
	g.ensureCircuitBreakersLocked()
	cb := g.circuitBreakers[key]
	g.mu.Unlock()
	if cb == nil {
		t.Fatalf("no circuit breaker configured for target %q", key)
	}
	return cb
}

func assertNoCallbackErrors(t *testing.T, callbackErrs <-chan error) {
	t.Helper()
	for {
		select {
		case err := <-callbackErrs:
			t.Error(err)
		default:
			return
		}
	}
}

func TestGateway_RouteStream_BeforePluginCanSetNilRequest(t *testing.T) {
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "missing"}},
	})

	_ = gw.RegisterPlugin(plugin.StageBeforeRequest, &testPlugin{
		name: "nil-request",
		typ:  plugin.TypeGuardrail,
		execFn: func(_ context.Context, pctx *plugin.Context) error {
			pctx.Request = nil
			return nil
		},
	})

	_, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for missing streaming provider")
	}
}

func TestGateway_RouteStream_RunAfterReceivesStreamResponse(t *testing.T) {
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
	})

	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: mockProviderName, models: []string{"gpt-4o"}},
		streamFn: func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
			ch := make(chan providers.StreamChunk, 3)
			ch <- providers.StreamChunk{
				ID:      "chatcmpl-stream",
				Object:  "chat.completion.chunk",
				Created: 123,
				Model:   "gpt-4o",
				Choices: []providers.StreamChoice{{
					Index: 0,
					Delta: providers.MessageDelta{Role: "assistant", Content: "hello "},
				}},
			}
			ch <- providers.StreamChunk{
				ID:      "chatcmpl-stream",
				Object:  "chat.completion.chunk",
				Created: 123,
				Model:   "gpt-4o",
				Choices: []providers.StreamChoice{{
					Index:        0,
					Delta:        providers.MessageDelta{Content: "world"},
					FinishReason: "stop",
				}},
			}
			ch <- providers.StreamChunk{
				ID:      "chatcmpl-stream",
				Object:  "chat.completion.chunk",
				Created: 123,
				Model:   "gpt-4o",
				Usage:   &providers.Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5},
			}
			close(ch)
			return ch, nil
		},
	})

	var afterCalls int
	callbackErrs := make(chan error, 8)
	_ = gw.RegisterPlugin(plugin.StageAfterRequest, &testPlugin{
		name: "after",
		typ:  plugin.TypeLogging,
		execFn: func(_ context.Context, pctx *plugin.Context) error {
			afterCalls++
			if pctx.Request == nil {
				callbackErrs <- errors.New("after plugin request is nil")
			}
			if pctx.Response == nil {
				callbackErrs <- errors.New("after plugin response is nil")
				return nil
			}
			if pctx.Response.Provider != mockProviderName {
				callbackErrs <- fmt.Errorf("after plugin provider = %q, want mock", pctx.Response.Provider)
			}
			if pctx.Response.Model != "gpt-4o" {
				callbackErrs <- fmt.Errorf("after plugin model = %q, want gpt-4o", pctx.Response.Model)
			}
			if pctx.Response.Usage.TotalTokens != 5 {
				callbackErrs <- fmt.Errorf("after plugin total tokens = %d, want 5", pctx.Response.Usage.TotalTokens)
			}
			if len(pctx.Response.Choices) == 0 {
				callbackErrs <- errors.New("after plugin choices are empty")
			} else if got := pctx.Response.Choices[0].Message.Content; got != "hello world" {
				callbackErrs <- fmt.Errorf("after plugin content = %q, want hello world", got)
			}
			return nil
		},
	})

	ch, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
		Stream:   true,
	})
	if err != nil {
		t.Fatalf("RouteStream: %v", err)
	}
	drainStream(t, ch)
	assertNoCallbackErrors(t, callbackErrs)

	if afterCalls != 1 {
		t.Fatalf("after plugin calls = %d, want 1", afterCalls)
	}
}

func TestGateway_RouteStream_RunOnErrorReceivesStreamError(t *testing.T) {
	streamErr := errors.New("stream failed")
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
	})

	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: mockProviderName, models: []string{"gpt-4o"}},
		streamFn: func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
			ch := make(chan providers.StreamChunk, 1)
			ch <- providers.StreamChunk{Error: streamErr}
			close(ch)
			return ch, nil
		},
	})

	var onErrorCalls int
	callbackErrs := make(chan error, 2)
	_ = gw.RegisterPlugin(plugin.StageOnError, &testPlugin{
		name: "on-error",
		typ:  plugin.TypeLogging,
		execFn: func(_ context.Context, pctx *plugin.Context) error {
			onErrorCalls++
			if !errors.Is(pctx.Error, streamErr) {
				callbackErrs <- fmt.Errorf("on-error plugin error = %v, want %v", pctx.Error, streamErr)
			}
			if pctx.Response != nil {
				callbackErrs <- fmt.Errorf("on-error plugin response = %#v, want nil", pctx.Response)
			}
			return nil
		},
	})

	ch, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
		Stream:   true,
	})
	if err != nil {
		t.Fatalf("RouteStream: %v", err)
	}
	for chunk := range ch {
		if !errors.Is(chunk.Error, streamErr) {
			t.Fatalf("stream chunk error = %v, want %v", chunk.Error, streamErr)
		}
	}
	assertNoCallbackErrors(t, callbackErrs)

	if onErrorCalls != 1 {
		t.Fatalf("on-error plugin calls = %d, want 1", onErrorCalls)
	}
}

func TestGateway_RouteStream_AfterPluginRejectRunsOnError(t *testing.T) {
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
	})

	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: mockProviderName, models: []string{"gpt-4o"}},
		streamFn: func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
			ch := make(chan providers.StreamChunk, 2)
			ch <- providers.StreamChunk{
				Model: "gpt-4o",
				Choices: []providers.StreamChoice{{
					Index:        0,
					Delta:        providers.MessageDelta{Role: "assistant", Content: "ok"},
					FinishReason: "stop",
				}},
			}
			ch <- providers.StreamChunk{
				Usage: &providers.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
			}
			close(ch)
			return ch, nil
		},
	})

	var onErrorCalls int
	callbackErrs := make(chan error, 1)
	_ = gw.RegisterPlugin(plugin.StageAfterRequest, &testPlugin{
		name: "after",
		typ:  plugin.TypeGuardrail,
		execFn: func(_ context.Context, pctx *plugin.Context) error {
			pctx.Reject = true
			pctx.Reason = "after plugin rejected"
			return nil
		},
	})
	_ = gw.RegisterPlugin(plugin.StageOnError, &testPlugin{
		name: "on-error",
		typ:  plugin.TypeLogging,
		execFn: func(_ context.Context, pctx *plugin.Context) error {
			onErrorCalls++
			var rejection *plugin.RejectionError
			if !errors.As(pctx.Error, &rejection) {
				callbackErrs <- fmt.Errorf("on-error plugin error = %T(%v), want *plugin.RejectionError", pctx.Error, pctx.Error)
			}
			return nil
		},
	})

	ch, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
		Stream:   true,
	})
	if err != nil {
		t.Fatalf("RouteStream: %v", err)
	}
	var sawPluginErr bool
	for chunk := range ch {
		if chunk.Error != nil {
			sawPluginErr = true
		}
	}
	if !sawPluginErr {
		t.Fatal("expected stream chunk carrying after plugin rejection error")
	}
	assertNoCallbackErrors(t, callbackErrs)
	if onErrorCalls != 1 {
		t.Fatalf("on-error plugin calls = %d, want 1", onErrorCalls)
	}
}

func TestGateway_Route_AfterPluginRejectRunsOnError(t *testing.T) {
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
	})

	gw.RegisterProvider(&mockProvider{
		name:   mockProviderName,
		models: []string{"gpt-4o"},
		resp:   &providers.Response{ID: "r1", Model: "gpt-4o", Provider: mockProviderName},
	})

	var onErrorCalls int
	_ = gw.RegisterPlugin(plugin.StageAfterRequest, &testPlugin{
		name: "after",
		typ:  plugin.TypeGuardrail,
		execFn: func(_ context.Context, pctx *plugin.Context) error {
			pctx.Reject = true
			pctx.Reason = "after plugin rejected"
			return nil
		},
	})
	_ = gw.RegisterPlugin(plugin.StageOnError, &testPlugin{
		name: "on-error",
		typ:  plugin.TypeLogging,
		execFn: func(_ context.Context, pctx *plugin.Context) error {
			onErrorCalls++
			var rejection *plugin.RejectionError
			if !errors.As(pctx.Error, &rejection) {
				t.Fatalf("on-error plugin error = %T(%v), want *plugin.RejectionError", pctx.Error, pctx.Error)
			}
			return nil
		},
	})

	_, err := gw.Route(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected after plugin rejection")
	}
	if onErrorCalls != 1 {
		t.Fatalf("on-error plugin calls = %d, want 1", onErrorCalls)
	}
}

func TestGateway_Route_AfterLoggingPanicStaysNonFatal(t *testing.T) {
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
	})

	gw.RegisterProvider(&mockProvider{
		name:   mockProviderName,
		models: []string{"gpt-4o"},
		resp:   &providers.Response{ID: "r1", Model: "gpt-4o", Provider: mockProviderName},
	})

	var onErrorCalls int
	_ = gw.RegisterPlugin(plugin.StageAfterRequest, &testPlugin{
		name: "after-logger",
		typ:  plugin.TypeLogging,
		execFn: func(context.Context, *plugin.Context) error {
			panic("log sink down")
		},
	})
	_ = gw.RegisterPlugin(plugin.StageOnError, &testPlugin{
		name: "on-error",
		typ:  plugin.TypeLogging,
		execFn: func(context.Context, *plugin.Context) error {
			onErrorCalls++
			return nil
		},
	})

	resp, err := gw.Route(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Route() error = %v, want nil", err)
	}
	if resp.ID != "r1" {
		t.Fatalf("response ID = %q, want r1", resp.ID)
	}
	if onErrorCalls != 0 {
		t.Fatalf("on-error plugin calls = %d, want 0", onErrorCalls)
	}
}

func TestGateway_RouteStream_AfterLoggingErrorStaysNonFatal(t *testing.T) {
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
	})

	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: mockProviderName, models: []string{"gpt-4o"}},
		streamFn: func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
			ch := make(chan providers.StreamChunk, 2)
			ch <- providers.StreamChunk{
				Model: "gpt-4o",
				Choices: []providers.StreamChoice{{
					Index:        0,
					Delta:        providers.MessageDelta{Role: "assistant", Content: "ok"},
					FinishReason: "stop",
				}},
			}
			ch <- providers.StreamChunk{
				Usage: &providers.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
			}
			close(ch)
			return ch, nil
		},
	})

	var onErrorCalls int
	_ = gw.RegisterPlugin(plugin.StageAfterRequest, &testPlugin{
		name: "after-logger",
		typ:  plugin.TypeLogging,
		execFn: func(context.Context, *plugin.Context) error {
			return fmt.Errorf("log sink down")
		},
	})
	_ = gw.RegisterPlugin(plugin.StageOnError, &testPlugin{
		name: "on-error",
		typ:  plugin.TypeLogging,
		execFn: func(context.Context, *plugin.Context) error {
			onErrorCalls++
			return nil
		},
	})

	ch, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
		Stream:   true,
	})
	if err != nil {
		t.Fatalf("RouteStream: %v", err)
	}
	var chunks int
	for chunk := range ch {
		chunks++
		if chunk.Error != nil {
			t.Fatalf("stream chunk error = %v, want nil", chunk.Error)
		}
	}
	if chunks == 0 {
		t.Fatal("expected stream chunks")
	}
	if onErrorCalls != 0 {
		t.Fatalf("on-error plugin calls = %d, want 0", onErrorCalls)
	}
}

func TestGateway_RouteStream_ResponseCacheHitSkipsProvider(t *testing.T) {
	gw, _ := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
	})

	var streamCalls atomic.Int32
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: mockProviderName, models: []string{"gpt-4o"}},
		streamFn: func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
			streamCalls.Add(1)
			ch := make(chan providers.StreamChunk, 2)
			ch <- providers.StreamChunk{
				ID:      "chatcmpl-cacheable",
				Object:  "chat.completion.chunk",
				Created: 123,
				Model:   "gpt-4o",
				Choices: []providers.StreamChoice{{
					Index:        0,
					Delta:        providers.MessageDelta{Role: "assistant", Content: "cached"},
					FinishReason: "stop",
				}},
			}
			ch <- providers.StreamChunk{
				ID:      "chatcmpl-cacheable",
				Object:  "chat.completion.chunk",
				Created: 123,
				Model:   "gpt-4o",
				Usage:   &providers.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
			}
			close(ch)
			return ch, nil
		},
	})
	cache := &cacheplugin.ResponseCache{}
	if err := cache.Init(map[string]any{"max_age": 60}); err != nil {
		t.Fatalf("cache init: %v", err)
	}
	_ = gw.RegisterPlugin(plugin.StageBeforeRequest, cache)
	_ = gw.RegisterPlugin(plugin.StageAfterRequest, cache)

	req := providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
		Stream:   true,
	}
	first, err := gw.RouteStream(context.Background(), req)
	if err != nil {
		t.Fatalf("first RouteStream: %v", err)
	}
	drainStream(t, first)
	if got := streamCalls.Load(); got != 1 {
		t.Fatalf("stream calls after first request = %d, want 1", got)
	}

	second, err := gw.RouteStream(context.Background(), req)
	if err != nil {
		t.Fatalf("second RouteStream: %v", err)
	}
	var content string
	var usage *providers.Usage
	for chunk := range second {
		if chunk.Error != nil {
			t.Fatalf("cached stream chunk error: %v", chunk.Error)
		}
		for _, choice := range chunk.Choices {
			content += choice.Delta.Content
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
	}
	if got := streamCalls.Load(); got != 1 {
		t.Fatalf("stream calls after cache hit = %d, want 1", got)
	}
	if content != "cached" {
		t.Fatalf("cached stream content = %q, want cached", content)
	}
	if usage == nil || usage.TotalTokens != 2 {
		t.Fatalf("cached stream usage = %#v, want total tokens 2", usage)
	}
}

func TestGateway_RouteStream_ContentBasedPromptRegex(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{
			Mode: config.ModeContentBased,
			ContentConditions: []config.ContentCondition{{
				Type:      "prompt_regex",
				Value:     `(?i)\b(code|function)\b`,
				TargetKey: "code-stream",
			}},
		},
		Targets: []config.Target{
			{VirtualKey: "general-stream"},
			{VirtualKey: "code-stream"},
		},
	})

	if err != nil {
		t.Fatal(err)
	}
	selected := make(chan string, 2)
	recordStream := func(name string) func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
		return func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
			selected <- name
			ch := make(chan providers.StreamChunk)
			close(ch)
			return ch, nil
		}
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{
			name:   "general-stream",
			models: []string{"gpt-4o"},
		},
		streamFn: recordStream("general-stream"),
	})
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{
			name:   "code-stream",
			models: []string{"gpt-4o"},
		},
		streamFn: recordStream("code-stream"),
	})

	out, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Stream:   true,
		Messages: []providers.Message{{Role: "user", Content: "write a Go function"}},
	})
	if err != nil {
		t.Fatalf("RouteStream: %v", err)
	}
	for range out { //nolint:revive // empty-block: intentionally draining the stream to completion
	}

	select {
	case got := <-selected:
		if got != "code-stream" {
			t.Fatalf("selected provider = %q, want code-stream", got)
		}
	default:
		t.Fatal("stream provider was not selected")
	}
}

func TestGateway_RouteStream_RetriesSynchronousStartBeforeFallback(t *testing.T) {
	firstCalls := 0
	secondCalls := 0
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets: []config.Target{
			{VirtualKey: "first", Retry: &config.RetryConfig{Attempts: 2, InitialBackoffMs: 1}},
			{VirtualKey: "second"},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "first", models: []string{"gpt-4o"}},
		streamFn: func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
			firstCalls++
			if firstCalls == 1 {
				return nil, errors.New("transient start failure")
			}
			ch := make(chan providers.StreamChunk)
			close(ch)
			return ch, nil
		},
	})
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "second", models: []string{"gpt-4o"}},
		streamFn: func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
			secondCalls++
			ch := make(chan providers.StreamChunk)
			close(ch)
			return ch, nil
		},
	})

	ch, err := gw.RouteStream(context.Background(), streamTestRequest())
	if err != nil {
		t.Fatalf("RouteStream: %v", err)
	}
	drainMeteredStream(t, ch)
	if firstCalls != 2 || secondCalls != 0 {
		t.Fatalf("start calls first=%d second=%d, want 2/0", firstCalls, secondCalls)
	}
}

func TestGateway_RouteStream_DoesNotReplayAfterChannelIsVisible(t *testing.T) {
	secondCalls := 0
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets:  []config.Target{{VirtualKey: "first"}, {VirtualKey: "second"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "first", models: []string{"gpt-4o"}},
		streamFn: func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
			ch := make(chan providers.StreamChunk, 2)
			ch <- providers.StreamChunk{Choices: []providers.StreamChoice{{Delta: providers.MessageDelta{Content: "visible"}}}}
			ch <- providers.StreamChunk{Error: errors.New("midstream failure")}
			close(ch)
			return ch, nil
		},
	})
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "second", models: []string{"gpt-4o"}},
		streamFn: func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
			secondCalls++
			ch := make(chan providers.StreamChunk)
			close(ch)
			return ch, nil
		},
	})

	ch, err := gw.RouteStream(context.Background(), streamTestRequest())
	if err != nil {
		t.Fatalf("RouteStream: %v", err)
	}
	drainMeteredStream(t, ch)
	if secondCalls != 0 {
		t.Fatalf("fallback replayed after client-visible stream start: second calls=%d", secondCalls)
	}
}

// TestGateway_RouteStream_HangingStartAbandonedAtRequestTimeout proves the
// start/retry phase is bounded by Config.RequestTimeout: a target configured
// with several retry attempts against a provider that never answers must not
// hold RouteStream open for anywhere near the full retry/backoff window (it
// would previously block until the caller's own context was cancelled).
func TestGateway_RouteStream_HangingStartAbandonedAtRequestTimeout(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		RequestTimeout: "50ms",
		Strategy:       config.StrategyConfig{Mode: config.ModeFallback},
		Targets: []config.Target{
			{VirtualKey: "hangs", Retry: &config.RetryConfig{Attempts: 5, InitialBackoffMs: 1}},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "hangs", models: []string{"gpt-4o"}},
		streamFn: func(ctx context.Context, _ providers.Request) (<-chan providers.StreamChunk, error) {
			<-ctx.Done() // simulate a provider that never answers on its own
			return nil, ctx.Err()
		},
	})

	// A generous bound so the test itself terminates even if the fix regresses;
	// the assertion below is what actually proves the abandonment is prompt.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	_, err = gw.RouteStream(ctx, streamTestRequest())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected RouteStream to fail once the hanging start is abandoned")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RouteStream error = %v, want a context-deadline error from the start-phase timeout", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("RouteStream took %s to abandon a hanging start with 5 configured retry attempts; want near the 50ms request timeout, not the full retry/backoff window", elapsed)
	}
}

// TestGateway_RouteStream_LiveStreamNotKilledByStartPhaseDeadline is the
// regression guard for the naive fix: RequestTimeout must bound only stream
// START, never a stream already visible to the caller. A provider that
// starts immediately but keeps sending well past RequestTimeout must drain
// without error.
func TestGateway_RouteStream_LiveStreamNotKilledByStartPhaseDeadline(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		RequestTimeout: "20ms",
		Strategy:       config.StrategyConfig{Mode: config.ModeSingle},
		Targets:        []config.Target{{VirtualKey: "slow-body"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "slow-body", models: []string{"gpt-4o"}},
		streamFn: func(ctx context.Context, _ providers.Request) (<-chan providers.StreamChunk, error) {
			ch := make(chan providers.StreamChunk, 1)
			go func() {
				defer close(ch)
				// Outlives RequestTimeout before sending its only chunk. If the
				// start-phase deadline leaked into this context instead of being
				// released once the channel became visible, ctx would already be
				// done by the time we get here.
				time.Sleep(100 * time.Millisecond)
				if ctx.Err() != nil {
					ch <- providers.StreamChunk{Error: errors.New("stream context was cancelled by the start-phase deadline")}
					return
				}
				ch <- providers.StreamChunk{Choices: []providers.StreamChoice{{Delta: providers.MessageDelta{Content: "still streaming"}}}}
			}()
			return ch, nil
		},
	})

	ch, err := gw.RouteStream(context.Background(), streamTestRequest())
	if err != nil {
		t.Fatalf("RouteStream: %v", err)
	}
	drainStream(t, ch)
}

func TestGateway_NewRejectsInvalidStreamingPromptRegex(t *testing.T) {
	_, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{
			Mode: config.ModeContentBased,
			ContentConditions: []config.ContentCondition{{
				Type:      "prompt_regex",
				Value:     `[invalid`,
				TargetKey: "code-stream",
			}},
		},
		Targets: []config.Target{{VirtualKey: "code-stream"}},
	})

	if err == nil {
		t.Fatal("expected invalid regex error")
	}
	if !strings.Contains(err.Error(), "invalid regex") {
		t.Fatalf("error = %v, want invalid regex", err)
	}
}

func TestGateway_ReloadConfigRejectsInvalidStreamingPromptRegex(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{
			Mode: config.ModeContentBased,
			ContentConditions: []config.ContentCondition{{
				Type:      "prompt_regex",
				Value:     `docs`,
				TargetKey: "general-stream",
			}},
		},
		Targets: []config.Target{{VirtualKey: "general-stream"}},
	})

	if err != nil {
		t.Fatal(err)
	}

	err = gw.ReloadConfig(context.Background(), config.Config{
		Strategy: config.StrategyConfig{
			Mode: config.ModeContentBased,
			ContentConditions: []config.ContentCondition{{
				Type:      "prompt_regex",
				Value:     `[invalid`,
				TargetKey: "general-stream",
			}},
		},
		Targets: []config.Target{{VirtualKey: "general-stream"}},
	})
	if err == nil {
		t.Fatal("expected invalid regex error")
	}
	if !strings.Contains(err.Error(), "invalid regex") {
		t.Fatalf("error = %v, want invalid regex", err)
	}
	if got := gw.config.Strategy.ContentConditions; len(got) != 1 || got[0].Value != "docs" {
		t.Fatalf("content conditions = %#v, want previous config to remain", got)
	}
}

func TestGateway_RouteStream_LeastLatencyUsesObservedP50(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeLatency},
		Targets: []config.Target{
			{VirtualKey: "slow"},
			{VirtualKey: "fast"},
		},
	})

	if err != nil {
		t.Fatal(err)
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "slow", models: []string{"gpt-4o"}},
		streamErr:    errors.New("slow provider selected"),
	})
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "fast", models: []string{"gpt-4o"}},
	})
	gw.latencyTracker.Record("slow", "gpt-4o", 120*time.Millisecond)
	gw.latencyTracker.Record("fast", "gpt-4o", 10*time.Millisecond)

	ch, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("RouteStream error = %v, want fast provider", err)
	}
	drainStream(t, ch)
}

func TestGateway_RouteStream_LeastLatencyRecordsStreamLatency(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeLatency},
		Targets:  []config.Target{{VirtualKey: "stream"}},
	})

	if err != nil {
		t.Fatal(err)
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "stream", models: []string{"gpt-4o"}},
	})

	ch, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("RouteStream error = %v", err)
	}
	drainStream(t, ch)

	if !gw.latencyTracker.HasSamples("stream") {
		t.Fatal("expected RouteStream to record a latency sample")
	}
}

func TestGateway_RouteStream_CostOptimizedUsesCatalogCost(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeCostOptimized},
		Targets: []config.Target{
			{VirtualKey: "expensive"},
			{VirtualKey: "cheap"},
		},
	})

	if err != nil {
		t.Fatal(err)
	}
	gw.catalog = models.Catalog{
		"expensive/gpt-4o": {
			Provider: "expensive",
			ModelID:  "gpt-4o",
			Mode:     models.ModeChat,
			Pricing: models.Pricing{
				InputPerMTokens: ptrFloat64(10),
			},
		},
		"cheap/gpt-4o": {
			Provider: "cheap",
			ModelID:  "gpt-4o",
			Mode:     models.ModeChat,
			Pricing: models.Pricing{
				InputPerMTokens: ptrFloat64(1),
			},
		},
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "expensive", models: []string{"gpt-4o"}},
		streamErr:    errors.New("expensive provider selected"),
	})
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "cheap", models: []string{"gpt-4o"}},
	})

	ch, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hello world"}},
	})
	if err != nil {
		t.Fatalf("RouteStream error = %v, want cheap provider", err)
	}
	drainStream(t, ch)
}

func TestGateway_RouteStream_CostOptimizedSkipErrorsWhenAllStreamCandidatesUnpriced(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{
			Mode:             config.ModeCostOptimized,
			UnpricedStrategy: config.UnpricedStrategySkip,
		},
		Targets: []config.Target{
			{VirtualKey: "first"},
			{VirtualKey: "second"},
		},
	})

	if err != nil {
		t.Fatal(err)
	}
	gw.catalog = models.Catalog{
		"first/gpt-4o": {
			Provider: "first",
			ModelID:  "gpt-4o",
			Mode:     models.ModeChat,
			Pricing:  models.Pricing{},
		},
		"second/gpt-4o": {
			Provider: "second",
			ModelID:  "gpt-4o",
			Mode:     models.ModeChat,
			Pricing:  models.Pricing{},
		},
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "first", models: []string{"gpt-4o"}},
		streamErr:    errors.New("first provider selected"),
	})
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "second", models: []string{"gpt-4o"}},
		streamErr:    errors.New("second provider selected"),
	})

	_, err = gw.RouteStream(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hello"}},
	})
	if err == nil {
		t.Fatal("expected RouteStream to reject unpriced providers in skip mode")
	}
	if !strings.Contains(err.Error(), "no priced provider supports model gpt-4o") {
		t.Fatalf("RouteStream error = %v, want no priced provider error", err)
	}
}

func TestGateway_StreamingCostOrderHandlesUnpricedStrategies(t *testing.T) {
	tests := []struct {
		name             string
		unpricedStrategy string
		catalog          models.Catalog
		want             []string
	}{
		{
			name:             "skip puts priced providers first",
			unpricedStrategy: config.UnpricedStrategySkip,
			catalog: models.Catalog{
				"unpriced/gpt-4o": {
					Provider: "unpriced",
					ModelID:  "gpt-4o",
					Mode:     models.ModeChat,
					Pricing:  models.Pricing{},
				},
				"priced/gpt-4o": {
					Provider: "priced",
					ModelID:  "gpt-4o",
					Mode:     models.ModeChat,
					Pricing: models.Pricing{
						InputPerMTokens: ptrFloat64(1),
					},
				},
			},
			want: []string{"priced", "unpriced", "missing", "plain"},
		},
		{
			name:             "allow keeps model-found unpriced providers eligible",
			unpricedStrategy: config.UnpricedStrategyAllow,
			catalog: models.Catalog{
				"unpriced/gpt-4o": {
					Provider: "unpriced",
					ModelID:  "gpt-4o",
					Mode:     models.ModeChat,
					Pricing:  models.Pricing{},
				},
				"priced/gpt-4o": {
					Provider: "priced",
					ModelID:  "gpt-4o",
					Mode:     models.ModeChat,
					Pricing: models.Pricing{
						InputPerMTokens: ptrFloat64(1),
					},
				},
			},
			want: []string{"unpriced", "priced", "missing", "plain"},
		},
		{
			name: "fallback returns target order when nothing is priced",
			catalog: models.Catalog{
				"unpriced/gpt-4o": {
					Provider: "unpriced",
					ModelID:  "gpt-4o",
					Mode:     models.ModeChat,
					Pricing:  models.Pricing{},
				},
			},
			want: []string{"unpriced", "priced", "missing", "plain"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gw, err := newTestGateway(t, config.Config{
				Strategy: config.StrategyConfig{
					Mode:             config.ModeCostOptimized,
					UnpricedStrategy: tt.unpricedStrategy,
				},
				Targets: []config.Target{
					{VirtualKey: "unpriced"},
					{VirtualKey: "priced"},
					{VirtualKey: "missing"},
					{VirtualKey: "plain"},
				},
			})

			if err != nil {
				t.Fatal(err)
			}
			gw.catalog = tt.catalog
			gw.RegisterProvider(&mockStreamProvider{
				mockProvider: mockProvider{name: "unpriced", models: []string{"gpt-4o"}},
			})
			gw.RegisterProvider(&mockStreamProvider{
				mockProvider: mockProvider{name: "priced", models: []string{"gpt-4o"}},
			})
			gw.RegisterProvider(&mockStreamProvider{
				mockProvider: mockProvider{name: "missing", models: []string{"gpt-4o"}},
			})
			gw.RegisterProvider(&mockProvider{
				name:   "plain",
				models: []string{"gpt-4o"},
			})

			got := streamTargetOrder(t, gw, providers.Request{
				Model:    "gpt-4o",
				Messages: []providers.Message{{Role: "user", Content: "hello world"}},
			})
			requireKeys(t, got, tt.want...)
		})
	}
}

func TestGateway_StreamingLatencyOrderFallsBackWithoutStreamingCandidates(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeLatency},
		Targets: []config.Target{
			{VirtualKey: "plain"},
			{VirtualKey: "unsupported"},
			{VirtualKey: "missing"},
		},
	})

	if err != nil {
		t.Fatal(err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   "plain",
		models: []string{"gpt-4o"},
	})
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "unsupported", models: []string{"other-model"}},
	})

	got := streamTargetOrder(t, gw, providers.Request{Model: "gpt-4o"})
	requireKeys(t, got, "plain", "unsupported", "missing")
}

func TestGateway_StreamingLatencyOrderTriesUnseenBeforeSampled(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeLatency},
		Targets: []config.Target{
			{VirtualKey: "unseen-a"},
			{VirtualKey: "sampled"},
			{VirtualKey: "unseen-b"},
		},
	})

	if err != nil {
		t.Fatal(err)
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "unseen-a", models: []string{"gpt-4o"}},
	})
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "sampled", models: []string{"gpt-4o"}},
	})
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "unseen-b", models: []string{"gpt-4o"}},
	})
	gw.latencyTracker.Record("sampled", "gpt-4o", 10*time.Millisecond)

	got := streamTargetOrder(t, gw, providers.Request{Model: "gpt-4o"})
	if got[2] != "sampled" {
		t.Fatalf("got keys %v, want sampled provider after unseen providers", got)
	}
	firstTwoUnseen := (got[0] == "unseen-a" && got[1] == "unseen-b") ||
		(got[0] == "unseen-b" && got[1] == "unseen-a")
	if !firstTwoUnseen {
		t.Fatalf("got keys %v, want unseen providers first", got)
	}
}

func TestGateway_StreamingCostOrderFallsBackWithoutStreamingCandidates(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeCostOptimized},
		Targets: []config.Target{
			{VirtualKey: "plain"},
			{VirtualKey: "unsupported"},
			{VirtualKey: "missing"},
		},
	})

	if err != nil {
		t.Fatal(err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   "plain",
		models: []string{"gpt-4o"},
	})
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "unsupported", models: []string{"other-model"}},
	})

	got := streamTargetOrder(t, gw, providers.Request{Model: "gpt-4o"})
	requireKeys(t, got, "plain", "unsupported", "missing")
}

// TestGateway_RouteStream_UnlistedProviderIsNotReachable is the streaming half
// of the allowlist guarantee asserted for embeddings and images in
// TestGateway_MultimodalUnlistedProviderIsNotReachable.
//
// Until v1.4.0 the streaming start fell back to any registered
// streaming-capable provider when no configured target was viable, so a
// provider the operator never listed could serve — and bill — a stream.
func TestGateway_RouteStream_UnlistedProviderIsNotReachable(t *testing.T) {
	called := false
	unlisted := &mockStreamProvider{
		mockProvider: mockProvider{name: "unlisted", models: []string{"stream-model"}},
		streamFn: func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
			called = true
			ch := make(chan providers.StreamChunk)
			close(ch)
			return ch, nil
		},
	}

	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		// The only target cannot stream, so no configured target is viable.
		Targets: []config.Target{{VirtualKey: "chat-only"}},
	})
	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}
	gw.RegisterProvider(&mockProvider{name: "chat-only", models: []string{"stream-model"}})
	gw.RegisterProvider(unlisted)

	_, streamErr := gw.RouteStream(context.Background(), providers.Request{
		Model:    "stream-model",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if streamErr == nil {
		t.Fatal("RouteStream succeeded; a provider outside targets must not serve the stream")
	}
	if called {
		t.Error("the unlisted provider was called — it is not a configured target")
	}
}

// The least-latency sample for a stream is the time to its first chunk, not
// the whole drain: a target whose answers are long is not a slow target, and
// ranking on drain time made verbose models look like slow providers.
func TestGateway_RouteStream_LatencySampleIsTimeToFirstChunk(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeLatency},
		Targets:  []config.Target{{VirtualKey: "stream"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	const drain = 150 * time.Millisecond
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "stream", models: []string{"gpt-4o"}},
		streamFn: func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
			ch := make(chan providers.StreamChunk)
			go func() {
				defer close(ch)
				ch <- providers.StreamChunk{ID: "first"}
				time.Sleep(drain)
				ch <- providers.StreamChunk{ID: "last"}
			}()
			return ch, nil
		},
	})

	ch, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("RouteStream error = %v", err)
	}
	drainStream(t, ch)

	p50, ok := gw.latencyTracker.Stats("stream", "gpt-4o")
	if !ok {
		t.Fatal("expected a latency sample for the stream")
	}
	if p50 >= drain {
		t.Fatalf("sample = %v, want the time to the first chunk, well under the %v drain", p50, drain)
	}
}

// targets[].timeout bounds a stream only until the provider answers. A stream
// that started inside the timeout must not be cut off by it mid-stream, since
// there is no replaying a half-delivered stream on another target.
func TestGateway_RouteStream_TargetTimeoutBoundsOnlyTheStart(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets:  []config.Target{{VirtualKey: "stream", Timeout: "20ms"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "stream", models: []string{"gpt-4o"}},
		streamFn: func(ctx context.Context, _ providers.Request) (<-chan providers.StreamChunk, error) {
			ch := make(chan providers.StreamChunk)
			go func() {
				defer close(ch)
				for i := range 3 {
					time.Sleep(30 * time.Millisecond) // each gap exceeds the attempt timeout
					select {
					case ch <- providers.StreamChunk{ID: fmt.Sprintf("c%d", i)}:
					case <-ctx.Done():
						ch <- providers.StreamChunk{Error: ctx.Err()}
						return
					}
				}
			}()
			return ch, nil
		},
	})

	ch, err := gw.RouteStream(context.Background(), providers.Request{
		Model:    "gpt-4o",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("RouteStream error = %v", err)
	}
	var got int
	for chunk := range ch {
		if chunk.Error != nil {
			t.Fatalf("stream cut off after %d chunks: %v", got, chunk.Error)
		}
		got++
	}
	if got != 3 {
		t.Fatalf("received %d chunks, want 3", got)
	}
}
