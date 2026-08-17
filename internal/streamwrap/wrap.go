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
	"time"

	"github.com/ferro-labs/ai-gateway/internal/events"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/pkg/metrics"
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
	// Model is the model ID after alias resolution. It reaches this struct as the
	// client supplied it, so it is used for cost lookup and event payloads but
	// never as a Prometheus label — see MetricModel.
	Model string
	// PriceProvider is the provider's canonical vendor identity — the name the
	// model catalog and price book are keyed on. Provider names the ROUTING
	// TARGET a stream used and stays on every metric label, event and the
	// Response it produces; PriceProvider exists solely so a target registered
	// under a routing alias (Gateway.RegisterProviderAs) is priced as its
	// underlying provider rather than against a name the catalog has never
	// heard of. Empty falls back to Provider, which is what every caller that
	// predates this field — registered under its own canonical name, where the
	// two already agree — continues to get.
	PriceProvider string
	// MetricModel is the bounded form of Model used for Prometheus labels: a
	// model the gateway cannot route collapses to metrics.UnknownModelLabel so a
	// client cannot mint unbounded time series. Required whenever Model can carry
	// a client-supplied value — leaving it empty degrades every label for the
	// request to "unknown" rather than falling back to the raw model.
	MetricModel string
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
	//
	// It receives the request's measurements because the after_request stage it
	// drives runs here and nowhere else: time to first token exists only on this
	// path, and it cannot be recovered once the channel has drained.
	CompletionFn func(ctx context.Context, resp *providers.Response, m Measurements) error
	// ErrorFn, if non-nil, is invoked once when the upstream stream fails or
	// the downstream client cancels before the stream completes.
	//
	// It receives the measurements taken up to the failure. A stream that failed
	// after eight seconds and one that failed instantly are different incidents,
	// and the on_error stage is the only place that can record which it was.
	ErrorFn func(ctx context.Context, err error, m Measurements)
	// CircuitBreakerOutcome, if non-nil, is invoked once when the stream
	// finishes. err is nil on success; non-nil on provider/stream failure.
	CircuitBreakerOutcome func(err error)
	// SuppressUsageForClient, when true, means the client explicitly opted
	// out of the usage chunk (stream_options.include_usage=false on the
	// incoming request — see providers/core.Request.ClientStreamOptions).
	// Meter clears the Usage field on the copy of each chunk forwarded to
	// out when this is set; it never affects accounting — the usage/cost
	// seen by CompletionFn, PublishFn, SpanFinisher, and Prometheus metrics
	// is always the real value the provider reported, captured before the
	// client-facing copy is stripped.
	//
	// The zero value (false) preserves pre-existing behaviour: the usage
	// chunk reaches the client whenever the provider sends one, matching
	// every caller that predates this field. Callers should set it to
	// `req.ClientStreamOptions != nil && !req.ClientStreamOptions.IncludeUsage`.
	SuppressUsageForClient bool
}

// metricLabelModel returns the bounded Prometheus label for this request.
//
// An unset MetricModel fails closed to UnknownModelLabel rather than falling
// back to the raw, client-supplied Model. Losing one label's precision is
// strictly better than letting a caller mint unbounded time series by omission.
func (m MeterMeta) metricLabelModel() string {
	if m.MetricModel == "" {
		return metrics.UnknownModelLabel
	}
	return m.MetricModel
}

// priceProviderName returns the catalog key this stream is priced against:
// PriceProvider when the caller resolved one, Provider otherwise. Every
// caller that predates PriceProvider leaves it unset and gets exactly the
// pricing behaviour it always had.
func (m MeterMeta) priceProviderName() string {
	if m.PriceProvider != "" {
		return m.PriceProvider
	}
	return m.Provider
}

// Measurements are the per-request numbers a completed stream produced. They
// mirror plugin.Measurements, which streamwrap cannot import without depending
// on the pipeline it is metered by.
type Measurements struct {
	DurationMs float64
	TTFTMs     float64
	TTLTMs     float64
	HasTTFT    bool
	CostUSD    float64
	HasCost    bool
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
	// Model is the model the PROVIDER reported, accumulated from the chunks —
	// the streaming counterpart of Response.Model, and the only way the span can
	// carry gen_ai.response.model, which is not knowable until chunks arrive.
	// Set on the success path only, matching the non-streaming path, which does
	// not claim a response model for a request that produced no response.
	Model string
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

// Meter wraps src and returns a new channel that forwards every StreamChunk,
// with one exception: when MeterMeta.SuppressUsageForClient is set, the Usage
// field is cleared on the forwarded copy of any chunk that carries it (the
// rest of the chunk — content, finish_reason, error — is untouched). Internal
// accounting always sees the real usage regardless. When a chunk carrying a
// non-nil Error is received, or when src
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
		resp := providers.Response{
			Object:   "chat.completion",
			Provider: meta.Provider,
			Model:    meta.Model,
		}

	loop:
		for {
			select {
			case <-ctx.Done():
				// The consumer (typically the HTTP handler) is gone. Stop trying
				// to forward chunks — out is almost certainly unread — but
				// keep draining src so the upstream provider goroutine can
				// finish its in-flight write to src and exit. The provider
				// MUST close src eventually for this to terminate; that is
				// the existing contract for every CompleteStream impl.
				//
				// WHY it is gone decides who is at fault: the caller hanging up,
				// or the gateway's idle bound firing on an upstream that sent
				// nothing. metrics.ProviderErrorType reads the cause and answers
				// that, for this path and every other surface at once.
				streamErr = drainSrc(ctx, src, streamErr)
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
				// disconnects mid-send. When the client opted out of usage
				// reporting, forward a copy with Usage cleared instead of
				// dropping the chunk outright — it may still carry content
				// or a finish_reason that must reach the client. Accounting
				// above already captured the real usage from the
				// unmodified chunk, so this never affects metrics/cost/plugins.
				forward := chunk
				if meta.SuppressUsageForClient && forward.Usage != nil {
					forward.Usage = nil
				}
				select {
				case out <- forward:
				case <-ctx.Done():
					streamErr = drainSrc(ctx, src, streamErr)
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
			finishStreamOnError(ctx, meta, usage, ttftMs, ttltMs, streamErr, latency, Measurements{
				DurationMs: float64(latency.Microseconds()) / 1000.0,
				TTFTMs:     ttftMs,
				TTLTMs:     ttltMs,
				HasTTFT:    !firstChunkAt.IsZero(),
			})
			return
		}

		if meta.LatencyRecorder != nil && meta.Provider != "" {
			meta.LatencyRecorder(meta.Provider, latency)
		}

		resp.Usage = usage
		if resp.Usage.TotalTokens == 0 {
			resp.Usage.TotalTokens = resp.Usage.PromptTokens + resp.Usage.CompletionTokens
		}
		// Priced before the completion callback, because the after_request
		// plugins it runs persist the cost and cannot compute it themselves.
		// The same result is handed to finishStreamOnSuccess so the recorded
		// figure and the reported one cannot diverge.
		cost := models.Calculate(meta.Catalog, meta.priceProviderName()+"/"+meta.Model, models.Usage{
			PromptTokens:     usage.PromptTokens,
			CompletionTokens: usage.CompletionTokens,
			ReasoningTokens:  usage.ReasoningTokens,
			CacheReadTokens:  usage.CacheReadTokens,
			CacheWriteTokens: usage.CacheWriteTokens,
		})
		measured := Measurements{
			DurationMs: float64(latency.Microseconds()) / 1000.0,
			TTFTMs:     ttftMs,
			TTLTMs:     ttltMs,
			// No chunk ever arrived, so there is no first token to have timed.
			HasTTFT: !firstChunkAt.IsZero(),
			CostUSD: cost.TotalUSD,
			// Priced, not ModelFound: a catalog entry carrying no input price
			// yields ModelFound=true with a total of zero, and reporting that as
			// a known cost files the request under the priced-$0 floor instead
			// of "cost unknown". About a fifth of the catalog is priced null —
			// ollama, self-hosted, free and preview entries — so ModelFound made
			// the spend total look complete while it was blind to all of them.
			HasCost: cost.Priced,
		}

		if handleCompletionFn(ctx, meta, usage, &resp, out, measured) {
			return
		}

		// Success path: emit the same metrics as Gateway.Route().
		finishStreamOnSuccess(ctx, meta, usage, resp.Model, ttftMs, ttltMs, latency, cost)
	}()

	return out
}

// drainSrc drains src to completion after the consumer has abandoned the
// stream, preserving the goroutine-leak guard: provider stream goroutines
// block on an unguarded `src <- chunk`, so src MUST be read until the provider
// closes it. streamErr is seeded from the context's cause when not already set,
// then overwritten by the last error chunk observed while draining, and returned.
//
// The cause rather than ctx.Err(): this error is what the request log and the
// span record, and "context canceled" describes the teardown rather than the
// fault that caused it. context.Cause degrades to exactly ctx.Err() when nothing
// named a reason, so this is never less specific than what it replaced.
func drainSrc(ctx context.Context, src <-chan providers.StreamChunk, streamErr error) error {
	if streamErr == nil {
		streamErr = context.Cause(ctx)
	}
	for chunk := range src {
		if chunk.Error != nil {
			streamErr = chunk.Error
		}
	}
	return streamErr
}

// finishStreamOnError emits error metrics, invokes error hooks, finalises the
// observability span, and records the circuit-breaker outcome. It is called
// exactly once when the stream loop exits with a non-nil streamErr.
func finishStreamOnError(
	ctx context.Context,
	meta MeterMeta,
	usage providers.Usage,
	ttftMs, ttltMs float64,
	streamErr error,
	latency time.Duration,
	measured Measurements,
) {
	errType := metrics.ProviderErrorType(ctx, streamErr)
	requestMetrics := metrics.ForRequest(meta.Provider, meta.metricLabelModel())
	// A stream that broke after eight seconds and one that broke instantly are
	// different incidents, and only this histogram can tell them apart. Observing
	// successes alone made the mid-stream failure — the streaming failure mode
	// that costs the most time — the one the quantiles could not see.
	requestMetrics.Duration.Observe(latency.Seconds())
	requestMetrics.Error.Inc()
	metrics.ForProviderError(meta.Provider, errType).Inc()
	if meta.PublishFn != nil {
		meta.PublishFn(ctx, events.FailedRequest(
			meta.TraceID,
			meta.Provider,
			meta.Model,
			streamErr,
			latency,
			true,
		))
	}
	if meta.ErrorFn != nil {
		meta.ErrorFn(ctx, streamErr, measured)
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
	resp *providers.Response,
	out chan<- providers.StreamChunk,
	measured Measurements,
) bool {
	if meta.CompletionFn == nil {
		return false
	}
	err := meta.CompletionFn(ctx, resp, measured)
	if err == nil {
		return false
	}
	requestMetrics := metrics.ForRequest(meta.Provider, meta.metricLabelModel())
	// The stream ran to completion and an after_request plugin then failed it.
	// The whole thing still took the time it took, so it is observed like any
	// other outcome — the same rule the non-streaming after-plugin failure
	// follows.
	requestMetrics.Duration.Observe(measured.DurationMs / 1000.0)
	requestMetrics.Error.Inc()
	metrics.ForProviderError(meta.Provider, metrics.ErrTypePlugin).Inc()
	select {
	case out <- providers.StreamChunk{Error: err}:
	case <-ctx.Done():
	}
	if meta.SpanFinisher != nil {
		meta.SpanFinisher.Finish(StreamOutcome{
			TokensIn:  usage.PromptTokens,
			TokensOut: usage.CompletionTokens,
			TTFTMs:    measured.TTFTMs,
			TTLTMs:    measured.TTLTMs,
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
	responseModel string,
	ttftMs, ttltMs float64,
	latency time.Duration,
	cost models.CostResult,
) {
	requestMetrics := metrics.ForRequest(meta.Provider, meta.metricLabelModel())
	requestMetrics.Duration.Observe(latency.Seconds())
	requestMetrics.Success.Inc()

	if usage.PromptTokens > 0 {
		requestMetrics.TokensIn.Add(float64(usage.PromptTokens))
	}
	if usage.CompletionTokens > 0 {
		requestMetrics.TokensOut.Add(float64(usage.CompletionTokens))
	}

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
			Model:       responseModel,
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
		for _, delta := range streamChoice.Delta.ToolCalls {
			choice.Message.ToolCalls = mergeToolCallDelta(choice.Message.ToolCalls, delta)
		}
		if streamChoice.FinishReason != "" {
			choice.FinishReason = streamChoice.FinishReason
		}
	}
}

// mergeToolCallDelta folds one streaming tool-call fragment into calls. The
// OpenAI streaming contract keys tool-call deltas by index: the first fragment
// for an index carries id/type/name and every later one appends argument text.
// Accumulating the fragments verbatim would hand after_request plugins, the
// request log, and cost accounting a response full of partial duplicates
// instead of the calls the model actually made.
//
// A fragment with no index cannot be keyed to an earlier one, so it is taken as
// a call in its own right — that is the shape providers use when they emit each
// tool call whole.
func mergeToolCallDelta(calls []providers.ToolCall, delta providers.ToolCall) []providers.ToolCall {
	if delta.Index != nil {
		for i := range calls {
			if calls[i].Index != nil && *calls[i].Index == *delta.Index {
				mergeToolCallInto(&calls[i], delta)
				return calls
			}
		}
	}
	return append(calls, delta)
}

// mergeToolCallInto applies one fragment to the call it belongs to. Identity
// fields are overwritten only when the fragment carries them, since they arrive
// once; arguments are the streamed part and concatenate.
func mergeToolCallInto(call *providers.ToolCall, delta providers.ToolCall) {
	if delta.ID != "" {
		call.ID = delta.ID
	}
	if delta.Type != "" {
		call.Type = delta.Type
	}
	if delta.Function.Name != "" {
		call.Function.Name = delta.Function.Name
	}
	call.Function.Arguments += delta.Function.Arguments
}
