// Package events defines compact internal hook event payloads for the gateway
// hot path and converts them to the public map form only at dispatch time.
package events

import (
	"time"

	"github.com/ferro-labs/ai-gateway/models"
)

// HookEvent is the typed internal representation of a gateway hook payload.
// The public hook API still receives a map, but the hot path can carry this
// compact value until dispatch time.
type HookEvent struct {
	Subject                 string
	TraceID                 string
	Provider                string
	Model                   string
	Error                   string
	Status                  int
	LatencyMs               int64
	Stream                  bool
	TokensIn                int
	TokensOut               int
	Cost                    models.CostResult
	Timestamp               time.Time
	IncludeExtendedCostKeys bool
}

// FailedRequest builds the internal hook payload for a failed request.
func FailedRequest(traceID, provider, model, errMsg string, latency time.Duration, stream bool) HookEvent {
	return HookEvent{
		Subject:   "gateway.request.failed",
		TraceID:   traceID,
		Provider:  provider,
		Model:     model,
		Error:     errMsg,
		Status:    500,
		LatencyMs: latency.Milliseconds(),
		Stream:    stream,
		Timestamp: time.Now(),
	}
}

// CompletedRequest builds the internal hook payload for a successful request.
func CompletedRequest(traceID, provider, model string, latency time.Duration, stream bool, tokensIn, tokensOut int, cost models.CostResult, includeExtendedCostKeys bool) HookEvent {
	return HookEvent{
		Subject:                 "gateway.request.completed",
		TraceID:                 traceID,
		Provider:                provider,
		Model:                   model,
		Status:                  200,
		LatencyMs:               latency.Milliseconds(),
		Stream:                  stream,
		TokensIn:                tokensIn,
		TokensOut:               tokensOut,
		Cost:                    cost,
		Timestamp:               time.Now(),
		IncludeExtendedCostKeys: includeExtendedCostKeys,
	}
}

// Map materializes the event into the public hook payload shape.
func (e HookEvent) Map() map[string]interface{} {
	if e.Error != "" {
		return map[string]interface{}{
			"trace_id":   e.TraceID,
			"provider":   e.Provider,
			"model":      e.Model,
			"error":      e.Error,
			"status":     e.Status,
			"latency_ms": e.LatencyMs,
			"stream":     e.Stream,
			"timestamp":  e.Timestamp,
		}
	}

	size := 16
	if e.IncludeExtendedCostKeys {
		size += 3
	}
	data := make(map[string]interface{}, size)
	data["trace_id"] = e.TraceID
	data["provider"] = e.Provider
	data["model"] = e.Model
	data["status"] = e.Status
	data["latency_ms"] = e.LatencyMs
	data["stream"] = e.Stream
	data["tokens_in"] = e.TokensIn
	data["tokens_out"] = e.TokensOut
	data["cost_usd"] = e.Cost.TotalUSD
	data["cost_input_usd"] = e.Cost.InputUSD
	data["cost_output_usd"] = e.Cost.OutputUSD
	data["cost_cache_read_usd"] = e.Cost.CacheReadUSD
	data["cost_cache_write_usd"] = e.Cost.CacheWriteUSD
	data["cost_reasoning_usd"] = e.Cost.ReasoningUSD
	data["cost_model_found"] = e.Cost.ModelFound
	data["timestamp"] = e.Timestamp

	if e.IncludeExtendedCostKeys {
		data["cost_image_usd"] = e.Cost.ImageUSD
		data["cost_audio_usd"] = e.Cost.AudioUSD
		data["cost_embedding_usd"] = e.Cost.EmbeddingUSD
	}

	return data
}
