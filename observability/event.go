package observability

import "time"

// RequestAttrs are the attributes attached to a root gateway request
// span by StartRequestSpan. They map to ferro.observability.v1 schema
// §5.1 (gen_ai.*) and §5.3 (ferro.*).
type RequestAttrs struct {
	// System is gen_ai.system, e.g. "openai", "anthropic", "bedrock".
	System string
	// Operation is gen_ai.operation.name: "chat", "embeddings",
	// "images.generate".
	Operation string
	// RequestModel is what the client asked for (may be an alias).
	RequestModel string
	// ResponseModel is what the provider actually used. Set later via
	// SetAttribute when known.
	ResponseModel string
	// IsStream is true for streaming requests.
	IsStream bool
	// RoutingStrategy is ferro.routing.strategy.
	RoutingStrategy string
	// TargetKey is ferro.routing.target_key (provider virtual key).
	TargetKey string
	// TraceID is the gateway's request trace ID (equal to the OTel
	// trace_id when OTel is active; equal to logging.TraceIDFromContext
	// in all cases).
	TraceID string
}

// CostBreakdown maps to ferro.cost.* span attributes and to the cost
// fields on Event.
type CostBreakdown struct {
	TotalUSD      float64
	InputUSD      float64
	OutputUSD     float64
	CacheReadUSD  float64
	CacheWriteUSD float64
	ReasoningUSD  float64
	// ModelFound is false when the cost calculator could not locate the
	// model in the pricing catalog (cost values will be zero).
	ModelFound bool
}

// Event is the payload broadcast to all registered Exporter plugins
// via Provider.RecordEvent. It mirrors the shape of
// internal/events.HookEvent but lives in the public package so plugin
// authors can consume it without importing internal/.
type Event struct {
	// Subject identifies the event kind, e.g.
	// "gateway.request.completed" or "gateway.request.failed".
	Subject string
	// TraceID is the gateway request trace ID.
	TraceID string
	// Provider is the resolved provider name.
	Provider string
	// Model is the resolved model name.
	Model string
	// Status is the HTTP-equivalent status (200, 500, …).
	Status int
	// Error is the error message for failed requests (already redacted).
	Error string
	// LatencyMs is the end-to-end gateway latency in milliseconds.
	LatencyMs int64
	// Stream indicates whether the request was streaming.
	Stream bool
	// TokensIn and TokensOut are the token counts reported by the
	// provider.
	TokensIn  int
	TokensOut int
	// Cost is the calculated cost breakdown.
	Cost CostBreakdown
	// Timestamp records when the event was constructed.
	Timestamp time.Time
	// Attributes carries additional ferro.* and gen_ai.* attributes that
	// don't fit into the typed fields above. Implementations MAY pass
	// this through to the backing system verbatim.
	Attributes map[string]any
}
