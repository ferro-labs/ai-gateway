//go:build integration
// +build integration

package strategies_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/observability"
	"github.com/ferro-labs/ai-gateway/pkg/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/test/testutil"
)

// The failover matrix pins the one rule every routing mode shares: which
// failures carry a request to a sibling target and which end it where it is.
// Both targets fail the same way in every cell, so the assertion — how many
// targets were called — does not depend on which one a random-draw mode
// picked first. Recovery on the sibling is proven separately, under fallback,
// where the order is the operator's.
//
// Saturation is deliberately absent: targets[].concurrency queues
// DefaultConcurrencyQueueSize requests before shedding, which no matrix cell
// should have to fill. The root package covers it with pipeline hooks.

// matrixStub serves chat, streaming, and embeddings for stratModel and fails
// them all the same way, so one table exercises every surface.
type matrixStub struct {
	miniStub
	calls atomic.Int64
	// fail returns the error every call answers with, or nil to succeed.
	fail func(ctx context.Context) error
	// midStream, when set, hands back a stream that fails after one chunk.
	midStream bool
}

var (
	_ core.Provider          = (*matrixStub)(nil)
	_ core.StreamProvider    = (*matrixStub)(nil)
	_ core.EmbeddingProvider = (*matrixStub)(nil)
)

func newMatrixStub(name string, fail func(ctx context.Context) error) *matrixStub {
	return &matrixStub{miniStub: miniStub{name: name, models: []string{stratModel}}, fail: fail}
}

func (s *matrixStub) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	s.calls.Add(1)
	if s.fail != nil {
		if err := s.fail(ctx); err != nil {
			return nil, err
		}
	}
	return okResponse(s.name, req), nil
}

func (s *matrixStub) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	s.calls.Add(1)
	if s.midStream {
		ch := make(chan core.StreamChunk, 2)
		ch <- core.StreamChunk{ID: s.name + "-1", Model: req.Model, Choices: []core.StreamChoice{{Delta: core.MessageDelta{Content: "partial"}}}}
		ch <- core.StreamChunk{Error: core.StatusError(s.name, 502, "upstream reset mid-stream")}
		close(ch)
		return ch, nil
	}
	if s.fail != nil {
		if err := s.fail(ctx); err != nil {
			return nil, err
		}
	}
	ch := make(chan core.StreamChunk, 1)
	ch <- core.StreamChunk{ID: s.name + "-1", Model: req.Model, Choices: []core.StreamChoice{{Delta: core.MessageDelta{Content: "ok from " + s.name}, FinishReason: "stop"}}}
	close(ch)
	return ch, nil
}

func (s *matrixStub) Embed(ctx context.Context, req core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	s.calls.Add(1)
	if s.fail != nil {
		if err := s.fail(ctx); err != nil {
			return nil, err
		}
	}
	return &core.EmbeddingResponse{
		Object: "list",
		Model:  req.Model,
		Data:   []core.Embedding{{Object: "embedding", Embedding: []float64{0.1, 0.2}}},
	}, nil
}

// attemptRecorder captures every observability event the gateway records so a
// cell can count physical attempts and read the A/B label on each.
type attemptRecorder struct {
	observability.Provider
	mu     sync.Mutex
	events []observability.Event
}

func newAttemptRecorder() *attemptRecorder {
	return &attemptRecorder{Provider: observability.NoOp()}
}

func (r *attemptRecorder) RecordEvent(_ context.Context, evt observability.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, evt)
}

func (r *attemptRecorder) RecordingEnabled() bool { return true }

func (r *attemptRecorder) RoutingAttemptsEnabled() bool { return true }

func (r *attemptRecorder) attempts() []observability.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []observability.Event
	for _, e := range r.events {
		if e.Subject == observability.SubjectRoutingAttempt {
			out = append(out, e)
		}
	}
	return out
}

// failoverClass is one column: what the selected target does with the call.
type failoverClass struct {
	name string
	// fail builds the error the target answers with. cancel ends the request
	// the way a departed client does; nil fail means the call succeeds.
	fail func(cancel context.CancelFunc) func(ctx context.Context) error
	// advances is whether a pool mode moves on to the sibling.
	advances bool
	// status is the upstream status a client is told when nobody recovers;
	// 0 means the class carries no HTTP status of its own.
	status int
	// retry configures the selected target's same-target retry policy; nil
	// leaves the default single attempt.
	retry *config.RetryConfig
	// callsPerTarget is how many times each attempted target is called.
	callsPerTarget int64
}

func statusClass(name string, status int, advances bool) failoverClass {
	return failoverClass{
		name:     name,
		status:   status,
		advances: advances,
		fail: func(context.CancelFunc) func(context.Context) error {
			return func(context.Context) error { return core.StatusError("stub", status, name) }
		},
		callsPerTarget: 1,
	}
}

func retryAfterClass(name string, wait time.Duration, callsPerTarget int64) failoverClass {
	return failoverClass{
		name:     name,
		status:   429,
		advances: true,
		fail: func(context.CancelFunc) func(context.Context) error {
			return func(context.Context) error {
				err := core.StatusError("stub", 429, name)
				err.RetryAfter = wait
				return err
			}
		},
		retry:          &config.RetryConfig{Attempts: 2, InitialBackoffMs: 1},
		callsPerTarget: callsPerTarget,
	}
}

var failoverClasses = []failoverClass{
	{name: "200", callsPerTarget: 1},
	{
		name:     "transport",
		advances: true,
		fail: func(context.CancelFunc) func(context.Context) error {
			return func(context.Context) error { return errors.New("dial tcp 10.0.0.1:443: connect: connection refused") }
		},
		callsPerTarget: 1,
	},
	// A target that accepted the connection and never answered: the provider
	// transport's ResponseHeaderTimeout ends the attempt with a
	// context.DeadlineExceeded the request never set. Only the request's own
	// deadline stops the walk, so this carries the request to the sibling.
	{
		name:     "attempt timeout",
		advances: true,
		fail: func(context.CancelFunc) func(context.Context) error {
			return func(context.Context) error {
				return fmt.Errorf("Post \"https://api.example.com/v1/chat/completions\": %w", context.DeadlineExceeded)
			}
		},
		callsPerTarget: 1,
	},
	statusClass("408", 408, true),
	statusClass("429", 429, true),
	// A short Retry-After is honoured as the same-target retry wait, so the
	// target is asked twice before the walk moves on.
	retryAfterClass("429 retry-after 50ms", 50*time.Millisecond, 2),
	// A Retry-After beyond the retry window abandons the target after one
	// call; the transient status still carries the request to the sibling.
	retryAfterClass("429 retry-after 120s", 120*time.Second, 1),
	statusClass("500", 500, true),
	statusClass("503", 503, true),
	statusClass("400", 400, false),
	statusClass("401", 401, false),
	statusClass("404", 404, false),
	statusClass("422", 422, false),
	{
		name: "client cancel",
		fail: func(cancel context.CancelFunc) func(context.Context) error {
			return func(ctx context.Context) error {
				cancel()
				return ctx.Err()
			}
		},
		callsPerTarget: 1,
	},
}

type routingMode struct {
	mode config.StrategyMode
	pool bool
	// prompt is what the request carries; content-based routes on it.
	prompt string
}

var routingModes = []routingMode{
	{mode: config.ModeSingle, prompt: "hello"},
	{mode: config.ModeFallback, pool: true, prompt: "hello"},
	{mode: config.ModeLoadBalance, pool: true, prompt: "hello"},
	{mode: config.ModeLatency, pool: true, prompt: "hello"},
	{mode: config.ModeCostOptimized, pool: true, prompt: "hello"},
	{mode: config.ModeABTest, pool: true, prompt: "hello"},
	{mode: config.ModeConditional, prompt: "hello"},
	{mode: config.ModeContentBased, prompt: "hello"},
}

// matrixConfig builds a two-target config for mode in which alpha is the named
// target under the named modes and both targets are eligible under the pool
// modes. retry, when set, applies to alpha and beta alike.
func matrixConfig(mode config.StrategyMode, retry *config.RetryConfig) config.Config {
	cfg := config.Config{
		Strategy: config.StrategyConfig{Mode: mode},
		Targets: []config.Target{
			{VirtualKey: "alpha", Weight: 1, Retry: retry},
			{VirtualKey: "beta", Weight: 1, Retry: retry},
		},
	}
	switch mode {
	case config.ModeABTest:
		cfg.Strategy.ABVariants = []config.ABVariantConfig{
			{TargetKey: "alpha", Weight: 1, Label: "control"},
			{TargetKey: "beta", Weight: 1, Label: "challenger"},
		}
	case config.ModeConditional:
		cfg.Strategy.Conditions = []config.Condition{{Key: config.ConditionKeyModel, Value: stratModel, TargetKey: "alpha"}}
	case config.ModeContentBased:
		cfg.Strategy.ContentConditions = []config.ContentCondition{{Type: config.ContentConditionPromptContains, Value: "hello", TargetKey: "alpha"}}
	}
	return cfg
}

type matrixCell struct {
	t     *testing.T
	gw    *aigateway.Gateway
	rec   *attemptRecorder
	alpha *matrixStub
	beta  *matrixStub
}

// setupCell boots one gateway for mode with both targets failing per class and
// returns the request context the class may cancel.
func setupCell(t *testing.T, rm routingMode, class failoverClass) (ctx context.Context, cell matrixCell) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	var fail func(context.Context) error
	if class.fail != nil {
		fail = class.fail(cancel)
	}
	alpha := newMatrixStub("alpha", fail)
	beta := newMatrixStub("beta", fail)

	gw, err := testutil.NewTestGateway(t, matrixConfig(rm.mode, class.retry))
	if err != nil {
		t.Fatalf("NewTestGateway(%s): %v", rm.mode, err)
	}
	rec := newAttemptRecorder()
	gw.SetObservability(rec)
	gw.RegisterProvider(alpha)
	gw.RegisterProvider(beta)
	return ctx, matrixCell{t: t, gw: gw, rec: rec, alpha: alpha, beta: beta}
}

// expectedTargets is how many distinct targets a cell reaches.
func expectedTargets(rm routingMode, class failoverClass) int64 {
	if class.fail == nil {
		return 1
	}
	if rm.pool && class.advances {
		return 2
	}
	return 1
}

// assertWalk checks the physical-call shape of one cell: how many targets were
// reached, how many calls each took, that every call produced exactly one
// attempt observation, and that only ab-test attempts carry a variant label.
func (c matrixCell) assertWalk(rm routingMode, class failoverClass) {
	c.t.Helper()
	wantTargets := expectedTargets(rm, class)
	wantCalls := wantTargets * class.callsPerTarget

	gotCalls := c.alpha.calls.Load() + c.beta.calls.Load()
	if gotCalls != wantCalls {
		c.t.Errorf("physical calls = %d (alpha=%d beta=%d), want %d", gotCalls, c.alpha.calls.Load(), c.beta.calls.Load(), wantCalls)
	}
	var reached int64
	if c.alpha.calls.Load() > 0 {
		reached++
	}
	if c.beta.calls.Load() > 0 {
		reached++
	}
	if reached != wantTargets {
		c.t.Errorf("targets reached = %d, want %d", reached, wantTargets)
	}

	attempts := c.rec.attempts()
	if int64(len(attempts)) != wantCalls {
		c.t.Errorf("attempt observations = %d, want %d (one per physical call)", len(attempts), wantCalls)
	}
	for i, evt := range attempts {
		if evt.RoutingAttempt == nil {
			c.t.Errorf("attempt %d carries no RoutingAttempt payload", i)
			continue
		}
		if evt.RoutingAttempt.Sequence != i+1 {
			c.t.Errorf("attempt %d sequence = %d, want %d", i, evt.RoutingAttempt.Sequence, i+1)
		}
		label, labelled := evt.Attributes[observability.AttrFerroRoutingABVariantLabel]
		if rm.mode == config.ModeABTest && !labelled {
			c.t.Errorf("attempt %d under ab-test carries no %s", i, observability.AttrFerroRoutingABVariantLabel)
		}
		if rm.mode != config.ModeABTest && labelled {
			c.t.Errorf("attempt %d under %s carries %s=%v", i, rm.mode, observability.AttrFerroRoutingABVariantLabel, label)
		}
	}
}

// assertOutcome checks what the client is told.
func assertOutcome(t *testing.T, class failoverClass, err error) {
	t.Helper()
	if class.fail == nil {
		if err != nil {
			t.Fatalf("healthy target: unexpected error %v", err)
		}
		return
	}
	if err == nil {
		t.Fatal("every target failed, yet the request succeeded")
	}
	if class.status != 0 {
		if got := core.ParseStatusCode(err); got != class.status {
			t.Errorf("client status = %d, want %d (err: %v)", got, class.status, err)
		}
	}
	if class.name == "client cancel" && !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled request error = %v, want context.Canceled", err)
	}
}

func TestStrategyMatrix_Chat(t *testing.T) {
	for _, rm := range routingModes {
		for _, class := range failoverClasses {
			t.Run(string(rm.mode)+"/"+class.name, func(t *testing.T) {
				ctx, cell := setupCell(t, rm, class)
				before := len(cell.gw.AllModels())

				resp, err := cell.gw.Route(ctx, core.Request{
					Model:    stratModel,
					Messages: []core.Message{{Role: "user", Content: rm.prompt}},
				})

				assertOutcome(t, class, err)
				cell.assertWalk(rm, class)
				if err == nil && resp.Provider != "alpha" && resp.Provider != "beta" {
					t.Errorf("served by %q, want alpha or beta", resp.Provider)
				}
				if after := len(cell.gw.AllModels()); after != before {
					t.Errorf("/v1/models inventory changed from %d to %d entries during routing", before, after)
				}
			})
		}
	}
}

func TestStrategyMatrix_StreamStart(t *testing.T) {
	for _, rm := range routingModes {
		for _, class := range failoverClasses {
			t.Run(string(rm.mode)+"/"+class.name, func(t *testing.T) {
				ctx, cell := setupCell(t, rm, class)

				ch, err := cell.gw.RouteStream(ctx, core.Request{
					Model:    stratModel,
					Stream:   true,
					Messages: []core.Message{{Role: "user", Content: rm.prompt}},
				})
				if err == nil {
					for range ch { //nolint:revive // empty-block: intentionally draining the stream to completion
					}
				}

				assertOutcome(t, class, err)
				cell.assertWalk(rm, class)
			})
		}
	}
}

func TestStrategyMatrix_Embeddings(t *testing.T) {
	for _, rm := range routingModes {
		for _, class := range failoverClasses {
			t.Run(string(rm.mode)+"/"+class.name, func(t *testing.T) {
				ctx, cell := setupCell(t, rm, class)

				_, err := cell.gw.Embed(ctx, core.EmbeddingRequest{Model: stratModel, Input: rm.prompt})

				assertOutcome(t, class, err)
				cell.assertWalk(rm, class)
			})
		}
	}
}

// Under fallback the order is the operator's, so recovery is exact: every
// transient class on the primary is answered by the secondary, every
// deterministic class is not.
func TestStrategyMatrix_FallbackRecoversOnSibling(t *testing.T) {
	for _, class := range failoverClasses {
		if class.fail == nil {
			continue
		}
		t.Run(class.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			alpha := newMatrixStub("alpha", class.fail(cancel))
			beta := newMatrixStub("beta", nil)

			gw, err := testutil.NewTestGateway(t, matrixConfig(config.ModeFallback, class.retry))
			if err != nil {
				t.Fatalf("NewTestGateway: %v", err)
			}
			gw.RegisterProvider(alpha)
			gw.RegisterProvider(beta)

			resp, err := gw.Route(ctx, core.Request{Model: stratModel, Messages: []core.Message{{Role: "user", Content: "hello"}}})

			if class.advances {
				if err != nil {
					t.Fatalf("transient %s on the primary should recover on the secondary, got %v", class.name, err)
				}
				if resp.Provider != "beta" {
					t.Errorf("served by %q, want beta", resp.Provider)
				}
				if beta.calls.Load() != 1 {
					t.Errorf("beta calls = %d, want 1", beta.calls.Load())
				}
			} else {
				if err == nil {
					t.Fatalf("deterministic %s on the primary must not be retried elsewhere", class.name)
				}
				if beta.calls.Load() != 0 {
					t.Errorf("beta was called %d times after a deterministic failure on alpha", beta.calls.Load())
				}
			}
			if alpha.calls.Load() != class.callsPerTarget {
				t.Errorf("alpha calls = %d, want %d", alpha.calls.Load(), class.callsPerTarget)
			}
		})
	}
}

// A stream that fails after its first byte is the client's to handle: the
// gateway has already committed to that target, so no sibling is tried.
func TestStrategyMatrix_NoFailoverAfterFirstByte(t *testing.T) {
	alpha := newMatrixStub("alpha", nil)
	alpha.midStream = true
	beta := newMatrixStub("beta", nil)

	gw, err := testutil.NewTestGateway(t, matrixConfig(config.ModeFallback, nil))
	if err != nil {
		t.Fatalf("NewTestGateway: %v", err)
	}
	gw.RegisterProvider(alpha)
	gw.RegisterProvider(beta)

	ch, err := gw.RouteStream(t.Context(), core.Request{Model: stratModel, Stream: true, Messages: []core.Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatalf("stream start: %v", err)
	}
	var sawContent, sawError bool
	for chunk := range ch {
		if chunk.Error != nil {
			sawError = true
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			sawContent = true
		}
	}
	if !sawContent || !sawError {
		t.Fatalf("stream delivered content=%v error=%v, want both", sawContent, sawError)
	}
	if beta.calls.Load() != 0 {
		t.Errorf("beta was called %d times after alpha failed mid-stream", beta.calls.Load())
	}
}

// An open circuit is a target the gateway already decided not to call, so
// every mode that offers a sibling serves from it instead — the rule-named
// modes included. single offers none: its strategy selects exactly one
// target, so the open circuit is refused there.
func TestStrategyMatrix_OpenCircuitIsSkippedUnderEveryMode(t *testing.T) {
	for _, rm := range routingModes {
		t.Run(string(rm.mode), func(t *testing.T) {
			alpha := newMatrixStub("alpha", func(context.Context) error {
				return core.StatusError("alpha", 500, "down")
			})
			beta := newMatrixStub("beta", nil)

			cfg := matrixConfig(rm.mode, nil)
			cfg.Targets[0].CircuitBreaker = &config.CircuitBreakerConfig{FailureThreshold: 1, SuccessThreshold: 1, Timeout: "1h"}
			gw, err := testutil.NewTestGateway(t, cfg)
			if err != nil {
				t.Fatalf("NewTestGateway: %v", err)
			}
			gw.RegisterProvider(alpha)
			gw.RegisterProvider(beta)

			req := core.Request{Model: stratModel, Messages: []core.Message{{Role: "user", Content: rm.prompt}}}
			// Trip alpha's breaker. A random-draw mode may hand the first
			// requests to beta, so route until alpha has failed once; the
			// threshold is one, so that single failure opens the circuit.
			for i := 0; alpha.calls.Load() == 0; i++ {
				if i == 50 {
					t.Fatalf("alpha was never selected in 50 requests under %s", rm.mode)
				}
				_, _ = gw.Route(t.Context(), req)
			}
			if state := gw.CircuitBreakerStates()["alpha"]; state != float64(circuitbreaker.StateOpen) {
				t.Fatalf("alpha breaker state = %v after a failure, want open (%v)", state, float64(circuitbreaker.StateOpen))
			}
			beforeAlpha := alpha.calls.Load()

			resp, err := gw.Route(t.Context(), req)

			if alpha.calls.Load() != beforeAlpha {
				t.Errorf("alpha was called through an open circuit")
			}
			// single names one target; a conditional or content-based rule with a
			// single target_key names one just as exactly (v1.5.2). The open
			// circuit is the corresponding error, never a sibling the rule did not
			// name — a rule that wants a stand-in lists one in target_keys.
			if rm.mode == config.ModeSingle || rm.mode == config.ModeConditional || rm.mode == config.ModeContentBased {
				if !errors.Is(err, circuitbreaker.ErrCircuitOpen) {
					t.Fatalf("%s names one target, so its open circuit must be refused; got err=%v", rm.mode, err)
				}
				if beta.calls.Load() != 0 {
					t.Errorf("beta was called %d times under %s", beta.calls.Load(), rm.mode)
				}
				return
			}
			if err != nil {
				t.Fatalf("with alpha's circuit open the request should be served by beta, got %v", err)
			}
			if resp.Provider != "beta" {
				t.Errorf("served by %q, want beta", resp.Provider)
			}
		})
	}
}
