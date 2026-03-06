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
	"github.com/ferro-labs/ai-gateway/internal/metrics"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/providers"
)

// MeterMeta carries the routing context needed to emit metrics once a stream
// finishes.
// Required fields: Provider, Model.
// Optional fields: Catalog (zero value disables cost reporting), PublishFn
// (nil disables event publishing), TraceID (empty value is allowed).
type MeterMeta struct {
	// Provider is the name of the provider that handled the request (e.g. "openai").
	Provider string
	// Model is the model ID after alias resolution.
	Model string
	// Catalog is a snapshot of the gateway's model catalog used for cost calculation.
	Catalog models.Catalog
	// PublishFn is the gateway's event-hook dispatcher. Called asynchronously on
	// stream completion or error.
	PublishFn func(ctx context.Context, subject string, data map[string]interface{})
	// TraceID is the per-request trace identifier, forwarded into events.
	TraceID string
}

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

		for chunk := range src {
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

		if streamErr != nil {
			errType := "provider_error"
			if errors.Is(streamErr, circuitbreaker.ErrCircuitOpen) {
				errType = "circuit_open"
			}
			metrics.RequestsTotal.WithLabelValues(meta.Provider, meta.Model, "error").Inc()
			metrics.ProviderErrors.WithLabelValues(meta.Provider, errType).Inc()
			if meta.PublishFn != nil {
				meta.PublishFn(ctx, "gateway.request.failed", map[string]interface{}{
					"trace_id":   meta.TraceID,
					"provider":   meta.Provider,
					"model":      meta.Model,
					"error":      streamErr.Error(),
					"status":     500,
					"latency_ms": latency.Milliseconds(),
					"stream":     true,
					"timestamp":  time.Now(),
				})
			}
			return
		}

		// Success path: emit the same metrics as Gateway.Route().
		metrics.RequestDuration.WithLabelValues(meta.Provider, meta.Model).Observe(latency.Seconds())
		metrics.RequestsTotal.WithLabelValues(meta.Provider, meta.Model, "success").Inc()

		if usage.PromptTokens > 0 {
			metrics.TokensInput.WithLabelValues(meta.Provider, meta.Model).Add(float64(usage.PromptTokens))
		}
		if usage.CompletionTokens > 0 {
			metrics.TokensOutput.WithLabelValues(meta.Provider, meta.Model).Add(float64(usage.CompletionTokens))
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
			metrics.RequestCostUSD.WithLabelValues(meta.Provider, meta.Model).Add(cost.TotalUSD)
		}

		if meta.PublishFn != nil {
			meta.PublishFn(ctx, "gateway.request.completed", map[string]interface{}{
				"trace_id":             meta.TraceID,
				"provider":             meta.Provider,
				"model":                meta.Model,
				"status":               200,
				"latency_ms":           latency.Milliseconds(),
				"stream":               true,
				"tokens_in":            usage.PromptTokens,
				"tokens_out":           usage.CompletionTokens,
				"cost_usd":             cost.TotalUSD,
				"cost_input_usd":       cost.InputUSD,
				"cost_output_usd":      cost.OutputUSD,
				"cost_cache_read_usd":  cost.CacheReadUSD,
				"cost_cache_write_usd": cost.CacheWriteUSD,
				"cost_reasoning_usd":   cost.ReasoningUSD,
				"cost_model_found":     cost.ModelFound,
				"timestamp":            time.Now(),
			})
		}
	}()

	return out
}
