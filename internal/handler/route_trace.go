package handler

import (
	"encoding/json"
	"net/http"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/apierror"
	"github.com/ferro-labs/ai-gateway/internal/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/ferro-labs/ai-gateway/internal/routetrace"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/providers"
)

// routeTraceRequest is the OpenAI chat/completions-shaped request accepted by
// POST /v1/route/trace. Only `model` is required — the messages array is
// accepted so callers can reuse a real completion payload, but the tracer
// never inspects message content (it routes on model only, matching the
// gateway's single/fallback/cost/latency strategies) and never calls a provider.
//
// An optional `metadata` object is accepted (and ignored) so Pi and other
// clients can pass their trace context without the gateway rejecting it.
type routeTraceRequest struct {
	Model    string              `json:"model"`
	Messages []providers.Message `json:"messages,omitempty"`
	Stream   bool                `json:"stream,omitempty"`
	Metadata map[string]any      `json:"metadata,omitempty"`
}

// RouteTrace handles POST /v1/route/trace — a dry-run route explanation.
//
// The endpoint performs NO upstream model call and consumes NO provider quota.
// It reproduces how the active routing strategy would SELECT a target for the
// request and returns the selected target/model, ordered candidate
// explanations, per-target health/circuit state, and model-catalog match state
// when available. It emits normal auth, audit, and OTel/request IDs like any
// other client-visible route because it is registered in the same authed
// /v1/* group as /v1/models and /v1/chat/completions.
func RouteTrace(gw *aigateway.Gateway) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req routeTraceRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			apierror.WriteOpenAI(w, http.StatusBadRequest, "invalid JSON body: "+err.Error(), "invalid_request_error", "invalid_body")
			return
		}
		if req.Model == "" {
			apierror.WriteOpenAI(w, http.StatusBadRequest, "field 'model' is required", "invalid_request_error", "missing_model")
			return
		}

		cfg := buildRouteTraceConfig(gw)
		resp := routetrace.Trace(cfg, req.Model)
		resp.TraceID = logging.TraceIDFromContext(r.Context())

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// buildRouteTraceConfig snapshots the gateway state the tracer needs. All
// accessors are MU-safe and non-mutating; this never increments a circuit
// counter, records a latency sample, or calls a provider.
func buildRouteTraceConfig(gw *aigateway.Gateway) routetrace.Config {
	gwCfg := gw.GetConfig()
	catalog := gw.Catalog()

	targets := make([]routetrace.Target, 0, len(gwCfg.Targets))
	for _, t := range gwCfg.Targets {
		targets = append(targets, routetrace.Target{
			VirtualKey: t.VirtualKey,
			Weight:     t.Weight,
		})
	}

	var conditions []routetrace.ConditionRule
	for _, cond := range gwCfg.Strategy.Conditions {
		conditions = append(conditions, routetrace.ConditionRule{
			Key:    cond.Key,
			Value:  cond.Value,
			Target: routetrace.Target{VirtualKey: cond.TargetKey},
		})
	}

	cfg := routetrace.Config{
		Mode:       routetrace.StrategyMode(gwCfg.Strategy.Mode),
		Targets:    targets,
		Conditions: conditions,
		Aliases:    gwCfg.Aliases,
		Catalog:    catalog,
		Lookup:     func(name string) (providers.Provider, bool) { return gw.Get(name) },
		CircuitState: func(name string) circuitbreaker.State {
			return gw.CircuitState(name)
		},
	}

	// Latency hooks (nil when least-latency routing is not active so the
	// tracer reports has_latency_samples=false for every candidate).
	if tracker := gw.LatencyTracker(); tracker != nil {
		cfg.LatencyP50 = func(name string) time.Duration { return tracker.P50(name) }
		cfg.LatencyHasSamps = func(name string) bool { return tracker.HasSamples(name) }
	}

	// Cost hook: per-target estimated cost for this model, derived from the
	// catalog via models.Calculate with a zero-usage probe. This is the same
	// primitive the cost-optimized strategy uses, so the trace's cost numbers
	// stay consistent with what the gateway would actually optimize on.
	cfg.CostForTarget = func(targetKey, modelID string) (models.CostResult, bool) {
		return models.Calculate(catalog, targetKey+"/"+modelID, models.Usage{}), true
	}

	return cfg
}
