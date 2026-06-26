// Package streamwrap provides a metering wrapper for streaming LLM responses.
// It transparently forwards SSE chunks while accumulating token-usage data and
// emitting the same Prometheus metrics and event hooks that non-streaming
// requests emit via Gateway.Route().
//
// # Stream-send drain contract (load-bearing invariant)
//
// Provider CompleteStream implementations spawn a goroutine that produces
// chunks with an UNGUARDED, blocking send: `ch <- chunk` (no select on
// ctx.Done(), no buffering guarantee). That goroutine therefore only stays
// leak-free as long as SOMETHING keeps reading ch until the provider closes
// it. Meter is that something: on consumer abandonment or context
// cancellation it does not simply stop — it ALWAYS continues to drain src to
// completion (`for range src`) so the blocked provider send can proceed and
// the provider goroutine can run to its `close(ch)` and exit.
//
// Consequence: any consumer that reads a provider stream channel DIRECTLY
// (bypassing Meter) and stops reading early — on client disconnect, an error,
// an early break, or a panic — will permanently block the provider's
// `ch <- chunk`, leaking that goroutine (and whatever it holds: the HTTP
// response body, connections, buffers) for the life of the process. Always
// route provider streams through Meter, or replicate its full drain-on-abort
// behaviour. Do not "optimise" Meter to stop draining src early; that would
// reintroduce the leak.
package streamwrap

import (
	"context"
	"errors"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/internal/events"
	"github.com/ferro-labs/ai-gateway/internal/metrics"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/providers"
)

// MeterMeta carries the routing context needed to emit metrics once a stream
// finishes.
// Required fields: Provider, Model.
// Optional fields: Catalog (zero value disables cost reporting), PublishFn
// (nil disables event publishing), TraceID (empty value is allowed),
// SpanFinisher (nil leaves observability span finalisation to the caller).
type MeterMeta struct {
	// Provider is the name of the provider that handled the request (e.g. "openai").
	Provider string
	// Model is the model ID after alias resolution.
	Model string
	// Catalog is a snapshot of the gateway's model catalog used for cost calculation.
	Catalog models.Catalog
	// PublishFn is the gateway's event-hook dispatcher. Called asynchronously on
	// stream completion or error.
	PublishFn func(ctx context.Context, event events.HookEvent)
	// TraceID is the per-request trace identifier, forwarded into events.
	TraceID string
	// LatencyRecorder, if non-nil, records successful stream latency for routing.
	LatencyRecorder func(provider string, latency time.Duration)
	// SpanFinisher, if non-nil, is invoked exactly once when the stream
	// completes (with final usage + cost + timings) or fails. The
	// gateway uses this to stamp the observability root span with the
	// numbers that are only known after the channel drains. The metric
	// type is intentionally a minimal local interface so streamwrap
	// stays decoupled from the public observability package.
	SpanFinisher SpanFinisher
	// CompletionFn, if non-nil, is invoked once after the upstream stream closes
	// successfully and before success metrics/events are emitted.
	CompletionFn func(ctx context.Context, resp *providers.Response) error
	// ErrorFn, if non-nil, is invoked once when the upstream stream fails or
	// the downstream client cancels before the stream completes.
	ErrorFn func(ctx context.Context, err error)
	// CircuitBreakerOutcome, if non-nil, is invoked once when the stream
	// finishes. err is nil on success; non-nil on provider/stream failure.
	CircuitBreakerOutcome func(err error)
}

// StreamOutcome bundles the values stamped onto the observability span
// at stream completion. ErrorMsg is non-empty only on the failure path.
type StreamOutcome struct {
	TokensIn    int
	TokensOut   int
	ReasoningIn int
	Cost        models.CostResult
	TTFTMs      float64
	TTLTMs      float64
	ErrorMsg    string
}

// SpanFinisher is implemented by the gateway-level observability span
// wrapper. wrap.Meter calls Finish once per request after the source
// channel drains. Implementations MUST call End() on the underlying
// span themselves; Meter does not double-end.
type SpanFinisher interface {
	Finish(StreamOutcome)
}

// SpanFinisherFunc is a function adapter for SpanFinisher.
type SpanFinisherFunc func(StreamOutcome)

// Finish implements SpanFinisher.
func (f SpanFinisherFunc) Finish(o StreamOutcome) { f(o) }

// Meter wraps src and returns a new channel that forwards every StreamChunk
// unchanged. When a chunk carrying a non-nil Error is received, or when src
// closes, the goroutine emits request duration, token, and cost metrics then
// closes the returned channel. On an error chunk the loop exits immediately
// after forwarding it; any further chunks queued in src are not consumed.
//
// Drain-on-abort invariant (do not remove): when the consumer goes away
// (ctx.Done) Meter stops forwarding to out but keeps draining src to
// completion. This is load-bearing — provider CompleteStream goroutines do an
// unguarded blocking `src <- chunk`, so they only avoid leaking because Meter
// guarantees src is read until the provider closes it. See the package doc
// "Stream-send drain contract" for the full rationale. A consumer reading a
// provider stream directly and stopping early would deadlock that provider
// goroutine.
//
// start should be the time.Now() captured immediately before the upstream
// CompleteStream call so that latency includes provider connection time.
func Meter(ctx context.Context, src <-chan providers.StreamChunk, start time.Time, meta MeterMeta) <-chan providers.StreamChunk {
	out := make(chan providers.StreamChunk)

	go func() {
		defer close(out)

		var usage providers.Usage
		var streamErr error
		var firstChunkAt time.Time
		var lastChunkAt time.Time
		clientCanceled := false
		resp := providers.Response{
			Object:   "chat.completion",
			Provider: meta.Provider,
			Model:    meta.Model,
		}

	loop:
		for {
			select {
			case <-ctx.Done():
				// Consumer (typically the HTTP handler) went away. Stop trying
				// to forward chunks — out is almost certainly unread — but
				// keep draining src so the upstream provider goroutine can
				// finish its in-flight write to src and exit. The provider
				// MUST close src eventually for this to terminate; that is
				// the existing contract for every CompleteStream impl.
				clientCanceled = true
				if streamErr == nil {
					streamErr = ctx.Err()
				}
				for chunk := range src {
					if chunk.Error != nil {
						streamErr = chunk.Error
					}
				}
				break loop
			case chunk, ok := <-src:
				if !ok {
					break loop
				}
				now := time.Now()
				if firstChunkAt.IsZero() {
					firstChunkAt = now
				}
				lastChunkAt = now

				// Capture the last non-zero usage block (the final OpenAI chunk
				// with include_usage=true has TotalTokens > 0; other providers
				// may set it differently).
				if chunk.Usage != nil && (chunk.Usage.TotalTokens > 0 || chunk.Usage.PromptTokens > 0) {
					usage = *chunk.Usage
				}
				applyChunkToResponse(&resp, chunk)
				if chunk.Error != nil {
					streamErr = chunk.Error
				}

				// Forward the chunk, but stop blocking if the consumer
				// disconnects mid-send.
				select {
				case out <- chunk:
				case <-ctx.Done():
					clientCanceled = true
					if streamErr == nil {
						streamErr = ctx.Err()
					}
					for chunk := range src {
						if chunk.Error != nil {
							streamErr = chunk.Error
						}
					}
					break loop
				}

				// Stop consuming src as soon as an error chunk is forwarded.
				// If the provider does not close src promptly we would
				// otherwise block here and never emit metrics or close out.
				if chunk.Error != nil {
					break loop
				}
			}
		}

		latency := time.Since(start)

		// Stream timings (relative to start). Zero when no chunks
		// arrived (the error-before-first-token case).
		var ttftMs, ttltMs float64
		if !firstChunkAt.IsZero() {
			ttftMs = float64(firstChunkAt.Sub(start).Microseconds()) / 1000.0
			ttltMs = float64(lastChunkAt.Sub(start).Microseconds()) / 1000.0
		}

		if streamErr != nil {
			finishStreamOnError(ctx, meta, usage, ttftMs, ttltMs, clientCanceled, streamErr, latency)
			return
		}

		if meta.LatencyRecorder != nil && meta.Provider != "" {
			meta.LatencyRecorder(meta.Provider, latency)
		}

		resp.Usage = usage
		if resp.Usage.TotalTokens == 0 {
			resp.Usage.TotalTokens = resp.Usage.PromptTokens + resp.Usage.CompletionTokens
		}
		if handleCompletionFn(ctx, meta, usage, ttftMs, ttltMs, &resp, out) {
			return
		}

		// Success path: emit the same metrics as Gateway.Route().
		finishStreamOnSuccess(ctx, meta, usage, ttftMs, ttltMs, latency)
	}()

	return out
}

// finishStreamOnError emits error metrics, invokes error hooks, finalises the
// observability span, and records the circuit-breaker outcome. It is called
// exactly once when the stream loop exits with a non-nil streamErr.
func finishStreamOnError(
	ctx context.Context,
	meta MeterMeta,
	usage providers.Usage,
	ttftMs, ttltMs float64,
	clientCanceled bool,
	streamErr error,
	latency time.Duration,
) {
	errType := "provider_error"
	switch {
	case clientCanceled:
		errType = "client_canceled"
	case errors.Is(streamErr, circuitbreaker.ErrCircuitOpen):
		errType = "circuit_open"
	}
	requestMetrics := metrics.ForRequest(meta.Provider, meta.Model)
	requestMetrics.Error.Inc()
	metrics.ForProviderError(meta.Provider, errType).Inc()
	if meta.PublishFn != nil {
		meta.PublishFn(ctx, events.FailedRequest(
			meta.TraceID,
			meta.Provider,
			meta.Model,
			streamErr.Error(),
			latency,
			true,
		))
	}
	if meta.ErrorFn != nil {
		meta.ErrorFn(ctx, streamErr)
	}
	if meta.SpanFinisher != nil {
		meta.SpanFinisher.Finish(StreamOutcome{
			TokensIn:  usage.PromptTokens,
			TokensOut: usage.CompletionTokens,
			TTFTMs:    ttftMs,
			TTLTMs:    ttltMs,
			ErrorMsg:  streamErr.Error(),
		})
	}
	if meta.CircuitBreakerOutcome != nil {
		meta.CircuitBreakerOutcome(streamErr)
	}
}

// handleCompletionFn invokes meta.CompletionFn when it is set. It returns
// true if the caller (the Meter goroutine) should return immediately, which
// happens when CompletionFn returns a non-nil error. On error it emits plugin
// error metrics, forwards an error chunk on out, finalises the span, and
// records a successful circuit-breaker outcome (the provider stream itself
// completed successfully; only the plugin failed).
func handleCompletionFn(
	ctx context.Context,
	meta MeterMeta,
	usage providers.Usage,
	ttftMs, ttltMs float64,
	resp *providers.Response,
	out chan<- providers.StreamChunk,
) bool {
	if meta.CompletionFn == nil {
		return false
	}
	err := meta.CompletionFn(ctx, resp)
	if err == nil {
		return false
	}
	requestMetrics := metrics.ForRequest(meta.Provider, meta.Model)
	requestMetrics.Error.Inc()
	metrics.ForProviderError(meta.Provider, "plugin_error").Inc()
	select {
	case out <- providers.StreamChunk{Error: err}:
	case <-ctx.Done():
	}
	if meta.SpanFinisher != nil {
		meta.SpanFinisher.Finish(StreamOutcome{
			TokensIn:  usage.PromptTokens,
			TokensOut: usage.CompletionTokens,
			TTFTMs:    ttftMs,
			TTLTMs:    ttltMs,
			ErrorMsg:  err.Error(),
		})
	}
	// Provider stream completed; plugin failure must not block CB recovery.
	if meta.CircuitBreakerOutcome != nil {
		meta.CircuitBreakerOutcome(nil)
	}
	return true
}

// finishStreamOnSuccess emits success metrics, publishes the completion event,
// finalises the observability span, and records a successful circuit-breaker
// outcome. It mirrors what Gateway.Route() does for non-streaming requests.
func finishStreamOnSuccess(
	ctx context.Context,
	meta MeterMeta,
	usage providers.Usage,
	ttftMs, ttltMs float64,
	latency time.Duration,
) {
	requestMetrics := metrics.ForRequest(meta.Provider, meta.Model)
	requestMetrics.Duration.Observe(latency.Seconds())
	requestMetrics.Success.Inc()

	if usage.PromptTokens > 0 {
		requestMetrics.TokensIn.Add(float64(usage.PromptTokens))
	}
	if usage.CompletionTokens > 0 {
		requestMetrics.TokensOut.Add(float64(usage.CompletionTokens))
	}

	// Compute and emit cost.
	cost := models.Calculate(meta.Catalog, meta.Provider+"/"+meta.Model, models.Usage{
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		ReasoningTokens:  usage.ReasoningTokens,
		CacheReadTokens:  usage.CacheReadTokens,
		CacheWriteTokens: usage.CacheWriteTokens,
	})
	if cost.TotalUSD > 0 {
		requestMetrics.CostUSD.Add(cost.TotalUSD)
	}

	if meta.PublishFn != nil {
		meta.PublishFn(ctx, events.CompletedRequest(
			meta.TraceID,
			meta.Provider,
			meta.Model,
			latency,
			true,
			usage.PromptTokens,
			usage.CompletionTokens,
			cost,
			false,
		))
	}
	if meta.SpanFinisher != nil {
		meta.SpanFinisher.Finish(StreamOutcome{
			TokensIn:    usage.PromptTokens,
			TokensOut:   usage.CompletionTokens,
			ReasoningIn: usage.ReasoningTokens,
			Cost:        cost,
			TTFTMs:      ttftMs,
			TTLTMs:      ttltMs,
		})
	}
	if meta.CircuitBreakerOutcome != nil {
		meta.CircuitBreakerOutcome(nil)
	}
}

func applyChunkToResponse(resp *providers.Response, chunk providers.StreamChunk) {
	if chunk.ID != "" && resp.ID == "" {
		resp.ID = chunk.ID
	}
	if chunk.Created != 0 && resp.Created == 0 {
		resp.Created = chunk.Created
	}
	if chunk.Model != "" {
		resp.Model = chunk.Model
	}
	for _, streamChoice := range chunk.Choices {
		idx := streamChoice.Index
		if idx < 0 {
			continue
		}
		for len(resp.Choices) <= idx {
			resp.Choices = append(resp.Choices, providers.Choice{
				Index: len(resp.Choices),
				Message: providers.Message{
					Role: "assistant",
				},
			})
		}
		choice := &resp.Choices[idx]
		if streamChoice.Delta.Role != "" {
			choice.Message.Role = streamChoice.Delta.Role
		}
		choice.Message.Content += streamChoice.Delta.Content
		if len(streamChoice.Delta.ToolCalls) > 0 {
			choice.Message.ToolCalls = append(choice.Message.ToolCalls, streamChoice.Delta.ToolCalls...)
		}
		if streamChoice.FinishReason != "" {
			choice.FinishReason = streamChoice.FinishReason
		}
	}
}
