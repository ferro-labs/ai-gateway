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
	// trace_id when OTel is active; equal to logger.TraceIDFromContext
	// in all cases).
	TraceID string
	// User, SessionID and Metadata are the request identity the gateway
	// resolved (see RequestIdentity): empty when the caller supplied none.
	// Recorded as enduser.id, session.id and ferro.request.metadata.<key>.
	User      string
	SessionID string
	Metadata  map[string]string
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

// RoutingAttemptOutcome is the stable outcome of one routed target invocation.
type RoutingAttemptOutcome string

const (
	// RoutingAttemptSuccess means the target invocation returned successfully.
	RoutingAttemptSuccess RoutingAttemptOutcome = "success"
	// RoutingAttemptError means the target invocation returned an error.
	RoutingAttemptError RoutingAttemptOutcome = "error"
)

// RoutingAttempt describes one invocation admitted to the routing resilience
// layer, including locally refused breaker and limiter invocations, so the
// count is of routing-layer calls, not of bytes that reached a provider. For a
// streamed request the attempt ends when the stream starts: a failure after
// the first byte belongs to the request's terminal event, because the walk is
// over by then and no other target is asked.
type RoutingAttempt struct {
	TargetKey      string
	Provider       string
	RoutedModel    string
	UpstreamModel  string
	Sequence       int
	TargetSequence int
	LatencyMs      int64
	Status         int
	Outcome        RoutingAttemptOutcome
	Error          string
	// User, SessionID and Metadata are the request identity this attempt was
	// made under (see RequestIdentity); the same values sit on the enclosing
	// Event, so a consumer reading only the payload still has them.
	User      string
	SessionID string
	Metadata  map[string]string
}

// Event is the payload broadcast to all registered Exporter plugins
// via Provider.RecordEvent. It mirrors the shape of
// internal/events.HookEvent but lives in the public package so plugin
// authors can consume it without importing internal/.
type Event struct {
	// Subject identifies the event kind: "gateway.request.completed" or
	// "gateway.request.failed" once per request, and SubjectRoutingAttempt
	// once per routing-layer invocation — locally refused calls included —
	// for consumers that opted in through RoutingAttemptExporter or
	// RoutingAttemptRecordingProvider.
	Subject string
	// TraceID is the gateway request trace ID.
	TraceID string
	// User, SessionID and Metadata are the request identity the request was
	// recorded under (see RequestIdentity). Empty when the caller supplied none.
	User      string
	SessionID string
	Metadata  map[string]string
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
	// RoutingAttempt is present only for SubjectRoutingAttempt events.
	RoutingAttempt *RoutingAttempt
	// Attributes carries additional ferro.* and gen_ai.* attributes that
	// don't fit into the typed fields above. Implementations MAY pass
	// this through to the backing system verbatim.
	Attributes map[string]any
}
