// Package streamwrap provides a metering wrapper for streaming LLM responses.
// It transparently forwards SSE chunks while accumulating token-usage data and
// emitting the same Prometheus metrics and event hooks that non-streaming
// requests emit via Gateway.Route().
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
	// SpanFinisher, if non-nil, is invoked exactly once when the stream
	// completes (with final usage + cost + timings) or fails. The
	// gateway uses this to stamp the observability root span with the
	// numbers that are only known after the channel drains. The metric
	// type is intentionally a minimal local interface so streamwrap
	// stays decoupled from the public observability package.
	SpanFinisher SpanFinisher
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

		for chunk := range src {
			now := time.Now()
			if firstChunkAt.IsZero() {
				firstChunkAt = now
			}
			lastChunkAt = now

			// Capture the last non-zero usage block (the final OpenAI chunk with
			// include_usage=true has TotalTokens > 0; other providers may set it
			// differently).
			if chunk.Usage != nil && (chunk.Usage.TotalTokens > 0 || chunk.Usage.PromptTokens > 0) {
				usage = *chunk.Usage
			}
			if chunk.Error != nil {
				streamErr = chunk.Error
			}
			out <- chunk
			// Stop consuming src as soon as an error chunk is forwarded. If the
			// provider does not close the channel promptly we would otherwise
			// block here and never emit metrics or close out.
			if streamErr != nil {
				break
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
			errType := "provider_error"
			if errors.Is(streamErr, circuitbreaker.ErrCircuitOpen) {
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
			if meta.SpanFinisher != nil {
				meta.SpanFinisher.Finish(StreamOutcome{
					TokensIn:  usage.PromptTokens,
					TokensOut: usage.CompletionTokens,
					TTFTMs:    ttftMs,
					TTLTMs:    ttltMs,
					ErrorMsg:  streamErr.Error(),
				})
			}
			return
		}

		// Success path: emit the same metrics as Gateway.Route().
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
	}()

	return out
}
