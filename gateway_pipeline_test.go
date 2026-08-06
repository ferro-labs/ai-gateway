package aigateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/internal/apierror"
	"github.com/ferro-labs/ai-gateway/pkg/circuitbreaker"
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
	if _, seen := gw.latencyTracker.Stats(mockProviderName); !seen {
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
