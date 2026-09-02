package aigateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/internal/apierror"
	"github.com/ferro-labs/ai-gateway/pkg/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/pkg/metrics"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/prometheus/client_golang/prometheus"
)

const pipelineModel = "pipeline-model-v1"

// countingProvider records how many times the upstream was actually called.
type countingProvider struct {
	mockProvider
	calls atomic.Int64
}

func newCountingProvider(name string, fn func() (*providers.Response, error)) *countingProvider {
	p := &countingProvider{}
	p.name = name
	p.models = []string{pipelineModel}
	p.completeFn = func(context.Context, providers.Request) (*providers.Response, error) {
		p.calls.Add(1)
		return fn()
	}
	return p
}

func pipelineRequest() providers.Request {
	return providers.Request{
		Model:    pipelineModel,
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	}
}

// TestRetryIsHonouredInEveryStrategyMode is F19/F41.
//
// targets[].retry used to be wired only into the fallback strategy, so the
// identical config meant three upstream attempts under `mode: fallback` and one
// under every other mode — with nothing logged and no caveat in the shipped
// example, which annotates weight's mode limitation two lines above. Retry is a
// property of a TARGET, not of the algorithm that picked it.
func TestRetryIsHonouredInEveryStrategyMode(t *testing.T) {
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
			// The condition-driven modes need a rule, and every rule here points
			// at the one target so the mode does not change WHICH target is
			// tried — only how many times it is asked.
			switch mode {
			case config.ModeConditional:
				cfg.Strategy.Conditions = []config.Condition{{Key: "model", Value: pipelineModel, TargetKey: mockProviderName}}
			case config.ModeContentBased:
				cfg.Strategy.ContentConditions = []config.ContentCondition{{Type: "prompt_contains", Value: "hi", TargetKey: mockProviderName}}
			case config.ModeABTest:
				cfg.Strategy.ABVariants = []config.ABVariantConfig{{TargetKey: mockProviderName, Weight: 1, Label: "only"}}
			}

			gw, err := newTestGateway(t, cfg)
			if err != nil {
				t.Fatalf("new gateway: %v", err)
			}
			p := newCountingProvider(mockProviderName, func() (*providers.Response, error) {
				return nil, core.StatusError(mockProviderName, http.StatusInternalServerError, "boom")
			})
			gw.RegisterProvider(p)

			if _, err := gw.Route(context.Background(), pipelineRequest()); err == nil {
				t.Fatal("expected the route to fail")
			}
			if got := p.calls.Load(); got != 3 {
				t.Errorf("mode %s made %d upstream attempts, want 3 — targets[].retry must not depend on the routing mode", mode, got)
			}
		})
	}
}

// A deterministic 4xx is still never retried: uniform retry means the policy
// applies in every mode, not that every failure is retryable.
func TestRetryLeavesDeterministicClientErrorsAlone(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeLoadBalance},
		Targets: []config.Target{{
			VirtualKey: mockProviderName,
			Weight:     1,
			Retry:      &config.RetryConfig{Attempts: 3, InitialBackoffMs: 1},
		}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	p := newCountingProvider(mockProviderName, func() (*providers.Response, error) {
		return nil, core.StatusError(mockProviderName, http.StatusBadRequest, "bad prompt")
	})
	gw.RegisterProvider(p)

	if _, err := gw.Route(context.Background(), pipelineRequest()); err == nil {
		t.Fatal("expected the route to fail")
	}
	if got := p.calls.Load(); got != 1 {
		t.Errorf("a 400 was attempted %d times, want 1", got)
	}
}

// TestRouteError_LabelsTheAttemptedProvider is F64.
//
// Route hardcoded "" as the provider on every non-streaming failure, so
// gateway_provider_errors_total and gateway_requests_total{status="error"}
// collapsed every provider into one unlabelled series — the one thing those
// series exist to distinguish.
func TestRouteError_LabelsTheAttemptedProvider(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   mockProviderName,
		models: []string{pipelineModel},
		err:    core.StatusError(mockProviderName, http.StatusInternalServerError, "boom"),
	})

	if _, err := gw.Route(context.Background(), pipelineRequest()); err == nil {
		t.Fatal("expected the route to fail")
	}

	if requestMetricLabelExists(t, "", pipelineModel, "error") {
		t.Error("a provider failure was recorded against provider=\"\"")
	}
	if !requestMetricLabelExists(t, mockProviderName, pipelineModel, "error") {
		t.Errorf("no error sample carries provider=%q", mockProviderName)
	}
}

// TestRouteError_ObservesDuration is F63.
//
// gateway_request_duration_seconds observed successes only, so a provider
// timing out at thirty seconds was invisible while cache hits pulled the
// distribution down. The histogram was read as "how fast is the gateway" and
// answered "how fast are the requests that worked".
func TestRouteError_ObservesDuration(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   mockProviderName,
		models: []string{pipelineModel},
		err:    core.StatusError(mockProviderName, http.StatusInternalServerError, "boom"),
	})

	labels := map[string]string{"provider": mockProviderName, "model": pipelineModel}
	before := durationSampleCount(t, labels)
	if _, err := gw.Route(context.Background(), pipelineRequest()); err == nil {
		t.Fatal("expected the route to fail")
	}
	if delta := durationSampleCount(t, labels) - before; delta != 1 {
		t.Errorf("failed request added %d duration samples, want 1", delta)
	}
}

// TestClassify_GatewayTimeoutIs504 is F44.
//
// The gateway's own request_timeout reported 500. An UPSTREAM 504 already
// classified correctly, so msgUpstreamTime existed and was simply unreachable
// from the deadline path — the one failure an operator fixes by raising a
// config value was reported as an internal error.
func TestClassify_GatewayTimeoutIs504(t *testing.T) {
	status, _, code := apierror.RouteErrorDetails(context.DeadlineExceeded)
	if status != http.StatusGatewayTimeout {
		t.Errorf("deadline exceeded classified as %d, want %d", status, http.StatusGatewayTimeout)
	}
	if code != "gateway_timeout" {
		t.Errorf("code = %q, want gateway_timeout", code)
	}
}

// A saturated target whose queue wait ends at the deadline is still 429: the
// gateway's own backpressure is the more specific account of what happened.
func TestClassify_SaturationOutranksTheDeadline(t *testing.T) {
	err := errors.Join(core.ErrProviderSaturated, context.DeadlineExceeded)
	if status, _, _ := apierror.RouteErrorDetails(err); status != http.StatusTooManyRequests {
		t.Errorf("saturation + deadline classified as %d, want 429", status)
	}
}

// TestClassify_OpenCircuitIs503 is F45.
//
// A fast-fail with zero upstream calls returned 500 while /readyz simultaneously
// and correctly returned 503. A 503 tells an SDK to back off; a 500 reads as an
// internal bug and sends an operator hunting for one.
func TestClassify_OpenCircuitIs503(t *testing.T) {
	status, _, code := apierror.RouteErrorDetails(circuitbreaker.ErrCircuitOpen)
	if status != http.StatusServiceUnavailable {
		t.Errorf("open circuit classified as %d, want %d", status, http.StatusServiceUnavailable)
	}
	if code != "upstream_unavailable" {
		t.Errorf("code = %q, want upstream_unavailable", code)
	}
}

// The end-to-end half of F45: an open circuit must reach Route as an error the
// classifier answers 503 for, through whatever wrapping the pipeline adds.
func TestRoute_OpenCircuitClassifiesAs503(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets: []config.Target{{
			VirtualKey:     mockProviderName,
			CircuitBreaker: &config.CircuitBreakerConfig{FailureThreshold: 1, SuccessThreshold: 1, Timeout: "1h"},
		}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   mockProviderName,
		models: []string{pipelineModel},
		err:    core.StatusError(mockProviderName, http.StatusInternalServerError, "boom"),
	})

	// First failure opens the circuit; the second request never reaches upstream.
	if _, err := gw.Route(context.Background(), pipelineRequest()); err == nil {
		t.Fatal("expected the first route to fail")
	}
	_, secondErr := gw.Route(context.Background(), pipelineRequest())
	if secondErr == nil {
		t.Fatal("expected the second route to fast-fail on the open circuit")
	}
	if !errors.Is(secondErr, circuitbreaker.ErrCircuitOpen) {
		t.Fatalf("second route error = %v, want one wrapping ErrCircuitOpen", secondErr)
	}
	if status, _, _ := apierror.RouteErrorDetails(secondErr); status != http.StatusServiceUnavailable {
		t.Errorf("open circuit reached the client as %d, want 503", status)
	}
}

// The end-to-end half of F44: the gateway's own request_timeout, not an
// upstream one, must reach the client as 504.
func TestRoute_RequestTimeoutClassifiesAs504(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy:       config.StrategyConfig{Mode: config.ModeSingle},
		Targets:        []config.Target{{VirtualKey: mockProviderName}},
		RequestTimeout: "20ms",
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   mockProviderName,
		models: []string{pipelineModel},
		completeFn: func(ctx context.Context, _ providers.Request) (*providers.Response, error) {
			<-ctx.Done()
			// context.Cause, not ctx.Err: net/http reports the CAUSE on a
			// cancelled request, so this — the gateway's own sentinel — is the
			// error a real provider hands back, and it is the shape the
			// classifier has to answer. A mock returning ctx.Err() would pass
			// against a classifier that only understands the stdlib sentinel.
			return nil, context.Cause(ctx)
		},
	})

	_, routeErr := gw.Route(context.Background(), pipelineRequest())
	if routeErr == nil {
		t.Fatal("expected the request to be cut at the deadline")
	}
	if status, _, _ := apierror.RouteErrorDetails(routeErr); status != http.StatusGatewayTimeout {
		t.Errorf("the gateway's own request_timeout reached the client as %d, want 504", status)
	}
}

// The gateway's own deadline sentinel must answer the ordinary "did this time
// out" question. It is the value net/http reports as the cause of a cancelled
// request, so a classifier that only knows context.DeadlineExceeded sees it and
// answers 500 — which is exactly what happened.
func TestErrRequestTimeout_IsADeadlineExceeded(t *testing.T) {
	if !errors.Is(ErrRequestTimeout, context.DeadlineExceeded) {
		t.Error("ErrRequestTimeout does not match context.DeadlineExceeded")
	}
	// The specific identity must survive, so the breaker can still tell the
	// gateway's deadline apart from the caller's.
	callerDeadline := fmt.Errorf("caller gave up: %w", context.DeadlineExceeded)
	if errors.Is(callerDeadline, ErrRequestTimeout) {
		t.Error("a caller-supplied deadline is indistinguishable from the gateway's own")
	}
}

// A non-fallback strategy commits to the target it chose. Falling through to
// somebody else's target when the chosen one cannot serve the request would
// make a mistyped variant or condition key a silent misroute rather than the
// 404 it is.
func TestPipeline_NonFallbackModeDoesNotRerouteToAnotherTarget(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{
			Mode:       config.ModeABTest,
			ABVariants: []config.ABVariantConfig{{TargetKey: "ghost", Weight: 1, Label: "only"}},
		},
		Targets: []config.Target{{VirtualKey: "ghost"}, {VirtualKey: mockProviderName}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	backstop := newCountingProvider(mockProviderName, func() (*providers.Response, error) {
		return &providers.Response{ID: "r1", Model: pipelineModel}, nil
	})
	gw.RegisterProvider(backstop)

	_, routeErr := gw.Route(context.Background(), pipelineRequest())
	if !errors.Is(routeErr, core.ErrNoCapableProvider) {
		t.Fatalf("Route = %v, want core.ErrNoCapableProvider", routeErr)
	}
	if got := backstop.calls.Load(); got != 0 {
		t.Errorf("the unselected target was called %d times; an a/b variant that cannot serve the request is a 404, not a reroute", got)
	}
}

// Fallback still advances: the pipeline's per-target commitment applies to
// single-target strategies only.
func TestPipeline_FallbackAdvancesPastAFailingTarget(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets:  []config.Target{{VirtualKey: "first"}, {VirtualKey: "second"}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	first := newCountingProvider("first", func() (*providers.Response, error) {
		return nil, core.StatusError("first", http.StatusInternalServerError, "boom")
	})
	second := newCountingProvider("second", func() (*providers.Response, error) {
		return &providers.Response{ID: "r1", Model: pipelineModel}, nil
	})
	gw.RegisterProvider(first)
	gw.RegisterProvider(second)

	resp, routeErr := gw.Route(context.Background(), pipelineRequest())
	if routeErr != nil {
		t.Fatalf("Route: %v", routeErr)
	}
	if resp.Provider != "second" {
		t.Errorf("resp.Provider = %q, want second", resp.Provider)
	}
	if first.calls.Load() != 1 || second.calls.Load() != 1 {
		t.Errorf("attempts: first=%d second=%d, want 1 and 1", first.calls.Load(), second.calls.Load())
	}
}

func TestPipeline_PoolModesAdvanceOnlyOnSafeFailures(t *testing.T) {
	poolModes := []config.StrategyMode{
		config.ModeFallback,
		config.ModeLoadBalance,
		config.ModeLatency,
		config.ModeCostOptimized,
		config.ModeABTest,
	}
	failures := []struct {
		name     string
		err      error
		attempts int64
	}{
		{name: "transport", err: errors.New("connection reset"), attempts: 2},
		{name: "request timeout", err: core.StatusError("first", http.StatusRequestTimeout, "timeout"), attempts: 2},
		{name: "rate limit", err: core.StatusError("first", http.StatusTooManyRequests, "limited"), attempts: 2},
		{name: "server error", err: core.StatusError("first", http.StatusBadGateway, "bad gateway"), attempts: 2},
		{name: "open circuit", err: fmt.Errorf("breaker: %w", circuitbreaker.ErrCircuitOpen), attempts: 1},
		{name: "saturation", err: fmt.Errorf("limiter: %w", core.ErrProviderSaturated), attempts: 1},
		// A target that accepted the connection and never answered ends the
		// attempt through the provider transport's ResponseHeaderTimeout, whose
		// error is a context.DeadlineExceeded the request never set. It is not
		// retried on the same target — a hung target stays hung — but the pool
		// must carry the request to a sibling: this is the failure failover
		// exists for.
		{name: "attempt timeout", err: transportHeaderTimeoutError(t), attempts: 1},
		// Likewise a cancellation raised inside the target's own call while the
		// request context is still live belongs to that target, not the caller.
		{name: "target-side cancellation", err: fmt.Errorf("provider: %w", context.Canceled), attempts: 1},
	}

	for _, mode := range poolModes {
		for _, failure := range failures {
			t.Run(string(mode)+"/"+failure.name, func(t *testing.T) {
				first := newCountingProvider("first", func() (*providers.Response, error) {
					return nil, failure.err
				})
				second := newCountingProvider("second", func() (*providers.Response, error) {
					return &providers.Response{ID: "ok", Model: pipelineModel}, nil
				})
				gw, err := newTestGateway(t, config.Config{Targets: []config.Target{
					{VirtualKey: "first", Retry: &config.RetryConfig{Attempts: 2, InitialBackoffMs: 1}},
					{VirtualKey: "second"},
				}})
				if err != nil {
					t.Fatalf("new gateway: %v", err)
				}
				gw.RegisterProvider(first)
				gw.RegisterProvider(second)

				_, target, routeErr := routeTargets(context.Background(), gw, targetPlan{
					keys: []string{"first", "second"}, model: pipelineModel, advance: advancesPastFailure(mode),
				}, pipelineRequest(), nil, completeChat)
				if routeErr != nil {
					t.Fatalf("routeTargets: %v", routeErr)
				}
				if target.key != "second" || first.calls.Load() != failure.attempts || second.calls.Load() != 1 {
					t.Errorf("target=%q calls=(%d,%d), want second and (%d,1)", target.key, first.calls.Load(), second.calls.Load(), failure.attempts)
				}
			})
		}
	}
}

func TestPipeline_PoolModesStopOnUnsafeFailures(t *testing.T) {
	poolModes := []config.StrategyMode{
		config.ModeFallback,
		config.ModeLoadBalance,
		config.ModeLatency,
		config.ModeCostOptimized,
		config.ModeABTest,
	}
	failures := []struct {
		name string
		err  error
	}{
		{name: "400", err: core.StatusError("first", http.StatusBadRequest, "bad request")},
		{name: "401", err: core.StatusError("first", http.StatusUnauthorized, "unauthorized")},
		{name: "403", err: core.StatusError("first", http.StatusForbidden, "forbidden")},
		{name: "404", err: core.StatusError("first", http.StatusNotFound, "not found")},
		{name: "422", err: core.StatusError("first", http.StatusUnprocessableEntity, "invalid")},
	}

	for _, mode := range poolModes {
		for _, failure := range failures {
			t.Run(string(mode)+"/"+failure.name, func(t *testing.T) {
				first := newCountingProvider("first", func() (*providers.Response, error) {
					return nil, failure.err
				})
				second := newCountingProvider("second", func() (*providers.Response, error) {
					return &providers.Response{ID: "unexpected", Model: pipelineModel}, nil
				})
				gw, err := newTestGateway(t, config.Config{Targets: []config.Target{
					{VirtualKey: "first"}, {VirtualKey: "second"},
				}})
				if err != nil {
					t.Fatalf("new gateway: %v", err)
				}
				gw.RegisterProvider(first)
				gw.RegisterProvider(second)

				_, target, routeErr := routeTargets(context.Background(), gw, targetPlan{
					keys: []string{"first", "second"}, model: pipelineModel, advance: advancesPastFailure(mode),
				}, pipelineRequest(), nil, completeChat)
				if routeErr == nil {
					t.Fatal("routeTargets succeeded")
				}
				wantErr := fmt.Sprintf("target first: %v", failure.err)
				if routeErr.Error() != wantErr {
					t.Errorf("error = %q, want %q", routeErr, wantErr)
				}
				if target.key != "first" || first.calls.Load() != 1 || second.calls.Load() != 0 {
					t.Errorf("target=%q calls=(%d,%d), want first and (1,0)", target.key, first.calls.Load(), second.calls.Load())
				}
			})
		}
	}
}

type sharedPolicyProvider struct {
	mockProvider
	err         error
	chatCalls   atomic.Int64
	streamCalls atomic.Int64
	embedCalls  atomic.Int64
}

func (p *sharedPolicyProvider) Complete(context.Context, providers.Request) (*providers.Response, error) {
	p.chatCalls.Add(1)
	if p.err != nil {
		return nil, p.err
	}
	return &providers.Response{Model: pipelineModel}, nil
}

func (p *sharedPolicyProvider) CompleteStream(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
	p.streamCalls.Add(1)
	if p.err != nil {
		return nil, p.err
	}
	ch := make(chan providers.StreamChunk)
	close(ch)
	return ch, nil
}

func (p *sharedPolicyProvider) Embed(context.Context, providers.EmbeddingRequest) (*providers.EmbeddingResponse, error) {
	p.embedCalls.Add(1)
	if p.err != nil {
		return nil, p.err
	}
	return &providers.EmbeddingResponse{Model: pipelineModel}, nil
}

func TestPipeline_SharedSurfacesApplySafeAdvanceAndUnsafeStop(t *testing.T) {
	surfaces := []struct {
		name  string
		call  func(*Gateway) error
		calls func(*sharedPolicyProvider) int64
	}{
		{name: "chat", call: func(g *Gateway) error {
			_, err := g.Route(context.Background(), providers.Request{Model: pipelineModel})
			return err
		}, calls: func(p *sharedPolicyProvider) int64 { return p.chatCalls.Load() }},
		{name: "stream-start", call: func(g *Gateway) error {
			ch, err := g.RouteStream(context.Background(), providers.Request{Model: pipelineModel, Stream: true})
			if err == nil {
				for chunk := range ch {
					_ = chunk
				}
			}
			return err
		}, calls: func(p *sharedPolicyProvider) int64 { return p.streamCalls.Load() }},
		{name: "embeddings", call: func(g *Gateway) error {
			_, err := g.Embed(context.Background(), providers.EmbeddingRequest{Model: pipelineModel, Input: "hi"})
			return err
		}, calls: func(p *sharedPolicyProvider) int64 { return p.embedCalls.Load() }},
	}

	for _, policy := range []struct {
		name        string
		err         error
		wantSuccess bool
		wantSecond  int64
	}{
		{name: "safe-503-advances", err: core.StatusError("first", http.StatusServiceUnavailable, "down"), wantSuccess: true, wantSecond: 1},
		{name: "unsafe-400-stops", err: core.StatusError("first", http.StatusBadRequest, "bad request")},
	} {
		for _, surface := range surfaces {
			t.Run(policy.name+"/"+surface.name, func(t *testing.T) {
				first := &sharedPolicyProvider{mockProvider: mockProvider{name: "first", models: []string{pipelineModel}}, err: policy.err}
				second := &sharedPolicyProvider{mockProvider: mockProvider{name: "second", models: []string{pipelineModel}}}
				gw, err := newTestGateway(t, config.Config{
					Strategy: config.StrategyConfig{Mode: config.ModeFallback},
					Targets:  []config.Target{{VirtualKey: "first"}, {VirtualKey: "second"}},
				})
				if err != nil {
					t.Fatalf("new gateway: %v", err)
				}
				gw.RegisterProvider(first)
				gw.RegisterProvider(second)

				callErr := surface.call(gw)
				if (callErr == nil) != policy.wantSuccess {
					t.Fatalf("error = %v, want success %v", callErr, policy.wantSuccess)
				}
				if got := surface.calls(first); got != 1 {
					t.Errorf("first calls = %d, want 1", got)
				}
				if got := surface.calls(second); got != policy.wantSecond {
					t.Errorf("second calls = %d, want %d", got, policy.wantSecond)
				}
			})
		}
	}
}

func TestPipeline_RetryAfterControlsRetryBeforeAdvancement(t *testing.T) {
	for _, tc := range []struct {
		name       string
		retryAfter time.Duration
		wantCalls  int64
	}{
		{name: "short hint retries target", retryAfter: time.Millisecond, wantCalls: 2},
		{name: "hint beyond cap abandons target", retryAfter: 31 * time.Second, wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := newCountingProvider("first", func() (*providers.Response, error) {
				return nil, (&core.HTTPStatusError{StatusCode: http.StatusTooManyRequests, Message: "limited", RetryAfter: tc.retryAfter})
			})
			second := newCountingProvider("second", func() (*providers.Response, error) {
				return &providers.Response{Model: pipelineModel}, nil
			})
			gw, err := newTestGateway(t, config.Config{
				Strategy: config.StrategyConfig{Mode: config.ModeFallback},
				Targets: []config.Target{
					{VirtualKey: "first", Retry: &config.RetryConfig{Attempts: 2, InitialBackoffMs: 1}},
					{VirtualKey: "second"},
				},
			})
			if err != nil {
				t.Fatalf("new gateway: %v", err)
			}
			gw.RegisterProvider(first)
			gw.RegisterProvider(second)
			if _, err := gw.Route(context.Background(), pipelineRequest()); err != nil {
				t.Fatalf("Route: %v", err)
			}
			if first.calls.Load() != tc.wantCalls || second.calls.Load() != 1 {
				t.Errorf("calls = (%d, %d), want (%d, 1)", first.calls.Load(), second.calls.Load(), tc.wantCalls)
			}
		})
	}
}

func TestPipeline_ExhaustedPoolReportsAggregateFailure(t *testing.T) {
	firstErr := errors.New("first failed")
	secondErr := errors.New("second failed")
	first := newCountingProvider("first", func() (*providers.Response, error) { return nil, firstErr })
	second := newCountingProvider("second", func() (*providers.Response, error) { return nil, secondErr })
	gw, err := newTestGateway(t, config.Config{Targets: []config.Target{
		{VirtualKey: "first"}, {VirtualKey: "second"},
	}})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	gw.RegisterProvider(first)
	gw.RegisterProvider(second)

	_, target, routeErr := routeTargets(context.Background(), gw, targetPlan{
		keys: []string{"first", "second"}, model: pipelineModel, advance: true,
	}, pipelineRequest(), nil, completeChat)
	if routeErr == nil {
		t.Fatal("routeTargets succeeded")
	}
	if want := "all providers failed: target second: second failed"; routeErr.Error() != want {
		t.Errorf("error = %q, want %q", routeErr, want)
	}
	if target.key != "second" || first.calls.Load() != 1 || second.calls.Load() != 1 {
		t.Errorf("target=%q calls=(%d,%d), want second and (1,1)", target.key, first.calls.Load(), second.calls.Load())
	}
}

// Once the request's own context has ended there is nobody left to serve, so
// the walk stops whatever the failing call reported — a transport error after
// the client hung up, or the deadline itself.
func TestPipeline_RequestContextEndStopsFailover(t *testing.T) {
	for _, tc := range []struct {
		name string
		// end brings the request context to its end from inside the first
		// target's call and returns the error that call reports.
		end func(t *testing.T) (context.Context, func() error)
	}{
		{
			name: "client hangs up",
			end: func(*testing.T) (context.Context, func() error) {
				ctx, cancel := context.WithCancel(context.Background())
				return ctx, func() error {
					cancel()
					return errors.New("connection reset")
				}
			},
		},
		{
			name: "request deadline passes",
			end: func(t *testing.T) (context.Context, func() error) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
				t.Cleanup(cancel)
				return ctx, func() error {
					<-ctx.Done()
					return fmt.Errorf("upstream: %w", ctx.Err())
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, fail := tc.end(t)
			first := newCountingProvider("first", func() (*providers.Response, error) {
				return nil, fail()
			})
			second := newCountingProvider("second", func() (*providers.Response, error) {
				return &providers.Response{ID: "unexpected", Model: pipelineModel}, nil
			})
			gw, err := newTestGateway(t, config.Config{Targets: []config.Target{
				{VirtualKey: "first"}, {VirtualKey: "second"},
			}})
			if err != nil {
				t.Fatalf("new gateway: %v", err)
			}
			gw.RegisterProvider(first)
			gw.RegisterProvider(second)

			_, target, routeErr := routeTargets(ctx, gw, targetPlan{
				keys: []string{"first", "second"}, model: pipelineModel, advance: true,
			}, pipelineRequest(), nil, completeChat)
			if routeErr == nil {
				t.Fatal("routeTargets succeeded")
			}
			if second.calls.Load() != 0 {
				t.Errorf("second target was attempted %d times after the request context ended", second.calls.Load())
			}
			if target.key != "first" {
				t.Errorf("failed target = %q, want first", target.key)
			}
		})
	}
}

// transportHeaderTimeoutError returns the error net/http hands a provider whose
// target accepted the connection and never answered: the transport's
// ResponseHeaderTimeout fired while the request's own context was still live.
func transportHeaderTimeoutError(t *testing.T) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	client := &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 10 * time.Millisecond}}
	resp, err := client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("expected the response header timeout to fire")
	}
	return err
}

// The pipeline records latency against the TARGET KEY, which is the key
// LeastLatency reads its samples back by. Recording the provider's own reported
// name instead would leave the tracker holding samples nothing ever queries.
func TestPipeline_RecordsLatencyAgainstTheTargetKey(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   mockProviderName,
		models: []string{pipelineModel},
		completeFn: func(context.Context, providers.Request) (*providers.Response, error) {
			time.Sleep(time.Millisecond)
			return &providers.Response{ID: "r1", Model: pipelineModel}, nil
		},
	})

	if _, err := gw.Route(context.Background(), pipelineRequest()); err != nil {
		t.Fatalf("Route: %v", err)
	}
	if _, seen := gw.latencyTracker.Stats(mockProviderName, pipelineModel); !seen {
		t.Errorf("no latency sample recorded for target %q", mockProviderName)
	}
}

// A provider that panics must still resolve the half-open probe Allow()
// admitted. resolveState only repairs Open→HalfOpen on a timer — it never
// repairs a HalfOpen circuit stuck at its probe cap — so a stranded probe
// rejects every later request for that target until the process restarts. The
// panic must also still reach the caller.
func TestPipeline_PanicDoesNotStrandTheHalfOpenProbe(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets: []config.Target{{
			VirtualKey:     "panicky",
			CircuitBreaker: &config.CircuitBreakerConfig{FailureThreshold: 5, SuccessThreshold: 1, Timeout: "1h"},
		}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   "panicky",
		models: []string{pipelineModel},
		completeFn: func(context.Context, providers.Request) (*providers.Response, error) {
			panic("provider exploded")
		},
	})

	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic was swallowed; it must still propagate to the caller")
			}
		}()
		_, _ = gw.Route(context.Background(), pipelineRequest())
	}()

	cb := gw.circuitBreakers["panicky"]
	if cb == nil {
		t.Fatal("expected a circuit breaker for the panicky target")
	}
	// The panic was recorded as a failure, so the breaker still admits requests
	// (threshold 5, one failure) rather than sitting on an unresolved probe.
	if !cb.Allow() {
		t.Error("the circuit rejects after a single panicking attempt; its half-open probe was stranded")
	}
}

// durationSampleCount reads gateway_request_duration_seconds' observation count
// for one provider/model pair.
func durationSampleCount(t *testing.T, labels map[string]string) uint64 {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "gateway_request_duration_seconds" {
			continue
		}
		for _, m := range mf.GetMetric() {
			match := true
			for _, l := range m.GetLabel() {
				if want, ok := labels[l.GetName()]; ok && want != l.GetValue() {
					match = false
					break
				}
			}
			if match {
				return m.GetHistogram().GetSampleCount()
			}
		}
	}
	return 0
}

// A pool mode picks its leading target for load, cost or latency reasons, from
// candidates the operator declared interchangeable. When that target is dead the
// request belongs to the pool, not to the corpse: every one of these modes must
// carry it to a healthy sibling.
//
// Asserted with NO circuit breaker configured, because breakers are opt-in and
// the shipped examples leave them off. A breaker makes the walk stop RETRYING a
// dead target; it is not what makes the walk find a live one, and relying on it
// meant a default deployment lost failover completely.
//
// Every request must succeed, not most: loadbalance rotates and least-latency
// ranks, so a single request can pick the healthy target by luck and report a
// pass over a pool that fails most of its traffic.
func TestPipeline_PoolModesAdvancePastADeadTargetWithoutABreaker(t *testing.T) {
	const requests = 20

	for _, mode := range []config.StrategyMode{
		config.ModeLoadBalance,
		config.ModeLatency,
		config.ModeCostOptimized,
		config.ModeABTest,
	} {
		t.Run(string(mode), func(t *testing.T) {
			cfg := config.Config{
				Strategy: config.StrategyConfig{Mode: mode},
				Targets: []config.Target{
					{VirtualKey: "dead", Weight: 1},
					{VirtualKey: "healthy", Weight: 1},
				},
			}
			if mode == config.ModeABTest {
				cfg.Strategy.ABVariants = []config.ABVariantConfig{
					{TargetKey: "dead", Weight: 100, Label: "control"},
					{TargetKey: "healthy", Weight: 0, Label: "challenger"},
				}
			}
			gw, err := newTestGateway(t, cfg)
			if err != nil {
				t.Fatalf("new gateway: %v", err)
			}
			gw.RegisterProvider(newCountingProvider("dead", func() (*providers.Response, error) {
				return nil, core.StatusError("dead", http.StatusInternalServerError, "connection refused")
			}))
			healthy := newCountingProvider("healthy", func() (*providers.Response, error) {
				return &providers.Response{ID: "r1", Model: pipelineModel}, nil
			})
			gw.RegisterProvider(healthy)

			var failures int
			for range requests {
				if _, routeErr := gw.Route(context.Background(), pipelineRequest()); routeErr != nil {
					failures++
				}
			}
			if failures != 0 {
				t.Errorf("%d/%d requests failed while a healthy sibling served the same model",
					failures, requests)
			}
			if healthy.calls.Load() == 0 {
				t.Error("the healthy target was never attempted")
			}
		})
	}
}

// The counterpart: a mode that names ONE target does not silently reroute. The
// operator wrote the rule, and answering it from somebody else's target would
// make the rule a suggestion.
func TestPipeline_NamedTargetModesDoNotReroute(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  config.StrategyConfig
	}{
		{name: "single", cfg: config.StrategyConfig{Mode: config.ModeSingle}},
		{name: "conditional", cfg: config.StrategyConfig{
			Mode: config.ModeConditional,
			Conditions: []config.Condition{
				{Key: "model", Value: pipelineModel, TargetKey: "dead"},
			},
		}},
		{name: "content-based", cfg: config.StrategyConfig{
			Mode: config.ModeContentBased,
			ContentConditions: []config.ContentCondition{
				{Type: "prompt_contains", Value: "hi", TargetKey: "dead"},
			},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gw, err := newTestGateway(t, config.Config{
				Strategy: tc.cfg,
				Targets: []config.Target{
					{VirtualKey: "dead", Weight: 1},
					{VirtualKey: "healthy", Weight: 1},
				},
			})
			if err != nil {
				t.Fatalf("new gateway: %v", err)
			}
			gw.RegisterProvider(newCountingProvider("dead", func() (*providers.Response, error) {
				return nil, core.StatusError("dead", http.StatusInternalServerError, "boom")
			}))
			healthy := newCountingProvider("healthy", func() (*providers.Response, error) {
				return &providers.Response{ID: "r1", Model: pipelineModel}, nil
			})
			gw.RegisterProvider(healthy)

			if _, routeErr := gw.Route(context.Background(), pipelineRequest()); routeErr == nil {
				t.Fatal("Route succeeded; a named-target mode must not reroute to another target")
			}
			if healthy.calls.Load() != 0 {
				t.Errorf("healthy target was attempted %d times; the rule named \"dead\"", healthy.calls.Load())
			}
		})
	}
}

// An after_request plugin that breaks fails a request the provider served. The
// failure is still a request the gateway answered, so it is timed and counted
// like any other — as a plugin failure, not as a provider error.
func TestRoute_AfterRequestPluginFailureIsCountedAndTimed(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mockProviderName}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   mockProviderName,
		models: []string{pipelineModel},
		resp:   &providers.Response{ID: "ok", Model: pipelineModel},
	})
	if err := gw.RegisterPlugin(plugin.StageAfterRequest, &testPlugin{
		name: "broken-guardrail",
		typ:  plugin.TypeGuardrail,
		execFn: func(context.Context, *plugin.Context) error {
			return errors.New("boom")
		},
	}); err != nil {
		t.Fatalf("register after plugin: %v", err)
	}

	labels := map[string]string{"provider": mockProviderName, "model": pipelineModel}
	handles := metrics.ForRequest(mockProviderName, pipelineModel)
	durationsBefore := durationSampleCount(t, labels)
	errorsBefore := counterValue(t, handles.Error)

	if _, err := gw.Route(context.Background(), pipelineRequest()); err == nil {
		t.Fatal("expected the after_request failure to fail the route")
	}

	if delta := durationSampleCount(t, labels) - durationsBefore; delta != 1 {
		t.Errorf("failed request added %d duration samples, want 1", delta)
	}
	if delta := counterValue(t, handles.Error) - errorsBefore; delta != 1 {
		t.Errorf("failed request added %v to the error counter, want 1", delta)
	}
}

// TestPipeline_AttributionCountsEveryAttempt: the attribution a handler reads
// back names the target that answered and counts every routing-layer attempt
// that preceded it, so a failover shows as two attempts on the response.
func TestPipeline_AttributionCountsEveryAttempt(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets:  []config.Target{{VirtualKey: "dead"}, {VirtualKey: "alive", ModelMap: map[string]string{pipelineModel: "alive-upstream"}}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	gw.RegisterProviderAs("dead", newCountingProvider("vendor-a", func() (*providers.Response, error) {
		return nil, core.StatusError("dead", http.StatusServiceUnavailable, "down")
	}))
	gw.RegisterProviderAs("alive", &mockProvider{name: "vendor-b", models: []string{"alive-upstream"}, resp: &providers.Response{ID: "ok"}})

	attribution := &RoutingAttribution{}
	ctx := WithRoutingAttribution(context.Background(), attribution)
	if _, err := gw.Route(ctx, pipelineRequest()); err != nil {
		t.Fatalf("Route: %v", err)
	}
	want := RoutingAttribution{Provider: "vendor-b", Target: "alive", Model: "alive-upstream", Attempts: 2}
	if *attribution != want {
		t.Fatalf("attribution = %+v, want %+v", *attribution, want)
	}
}

// A hung primary must not consume the whole request budget: targets[].timeout
// bounds one physical attempt, the walk advances, and the sibling answers.
func TestPipeline_TargetTimeoutAdvancesPastAHungPrimary(t *testing.T) {
	hungProvider := &mockProvider{name: "hung", models: []string{pipelineModel}, completeFn: func(ctx context.Context, _ providers.Request) (*providers.Response, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	alive := newCountingProvider("alive", func() (*providers.Response, error) {
		return &providers.Response{ID: "served-by-alive", Model: pipelineModel}, nil
	})
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets: []config.Target{
			{VirtualKey: "hung", Timeout: "30ms"},
			{VirtualKey: "alive"},
		},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	gw.RegisterProvider(hungProvider)
	gw.RegisterProvider(alive)

	started := time.Now()
	resp, err := gw.Route(context.Background(), pipelineRequest())
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if resp.ID != "served-by-alive" {
		t.Fatalf("served %q, want the sibling after the primary's attempt timed out", resp.ID)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("took %v; the attempt timeout must have bounded the hung primary", elapsed)
	}
}

// The end-to-end request_timeout stays authoritative: when it is shorter than
// a target's attempt timeout the request ends there, answered 504, and no
// sibling is asked.
func TestPipeline_RequestTimeoutOutranksTargetTimeout(t *testing.T) {
	hungProvider := &mockProvider{name: "hung", models: []string{pipelineModel}, completeFn: func(ctx context.Context, _ providers.Request) (*providers.Response, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	alive := newCountingProvider("alive", func() (*providers.Response, error) {
		return &providers.Response{ID: "unexpected", Model: pipelineModel}, nil
	})
	gw, err := newTestGateway(t, config.Config{
		RequestTimeout: "30ms",
		Strategy:       config.StrategyConfig{Mode: config.ModeFallback},
		Targets: []config.Target{
			{VirtualKey: "hung", Timeout: "5s"},
			{VirtualKey: "alive"},
		},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	gw.RegisterProvider(hungProvider)
	gw.RegisterProvider(alive)

	_, err = gw.Route(context.Background(), pipelineRequest())
	if status, _, _ := apierror.RouteErrorDetails(err); status != http.StatusGatewayTimeout {
		t.Fatalf("status %d (%v), want 504 from the request deadline", status, err)
	}
	if alive.calls.Load() != 0 {
		t.Fatalf("sibling was asked %d times after the request deadline passed", alive.calls.Load())
	}
}
