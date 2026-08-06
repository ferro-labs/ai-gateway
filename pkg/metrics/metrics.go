// Package metrics registers the Prometheus metrics used by the gateway.
// Import this package (via blank import) from the server entry point to
// register all metrics before the /metrics handler is mounted.
package metrics

import (
	"context"
	"errors"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/ferro-labs/ai-gateway/pkg/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// UnknownModelLabel is the bounded label used for rejected requests whose model is unknown.
const UnknownModelLabel = "unknown"

// UnknownToolLabel is the bounded label used for tool calls naming a tool no
// registered MCP server has ever advertised. Tool names arrive in model output,
// so an unrecognised one is collapsed to this constant rather than minting a
// new time series per hallucinated name.
const UnknownToolLabel = "unknown"

// NoProviderLabel is the "provider" label used for a request that ended before
// any provider was chosen — denied by a plugin, or naming a model no configured
// target serves.
//
// Such a request is still counted: it is one the gateway served an answer to,
// and how long it took to refuse belongs in the same histogram as how long a
// success took. What it has no honest value for is the provider, and the empty
// string is the wrong way to say so twice over. It is silently dropped by the
// `provider=~".+"` matcher almost every dashboard and alert is written with, so
// the requests vanish from exactly the panels built to watch them; and a blank
// label is indistinguishable from one a bug forgot to set. A named value says
// "deliberately none", selects cleanly, and stays bounded — one series per
// already-bounded model label, not one per caller.
//
// It is the same rule UnknownModelLabel applies to the model label in the same
// call: a label with no real value gets a constant, never a blank.
const NoProviderLabel = "none"

// CacheProviderLabel is the "provider" label for a request the response cache
// served without calling anyone.
//
// It is not the provider that produced the cached response. That provider was
// not called, and crediting it adds tokens it never generated, successes it
// never served, and sub-millisecond latency samples that pull its quantiles
// down — a hundred hits of a thousand-token prompt is a hundred thousand
// phantom input tokens on a provider that may be down. The response's
// provenance is still the priming provider, and the span, the log line and the
// request-log row all still say so; only the per-provider throughput series,
// which answer "what did this upstream actually do", read "cache".
//
// It doubles as the cache-hit rate, which nothing else measures.
const CacheProviderLabel = "cache"

// providerLabel bounds the "provider" label for the metric vectors keyed by it.
// Every handle in this package is resolved through it, so no surface can mint a
// provider="" series by passing one up.
func providerLabel(provider string) string {
	if provider == "" {
		return NoProviderLabel
	}
	return provider
}

// Request-level counters and histograms.
var (
	// RequestsTotal counts completed requests labelled by provider, model, and
	// outcome ("success", "error", "rejected").
	RequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_requests_total",
			Help: "Total number of requests processed by the gateway.",
		},
		[]string{"provider", "model", "status"},
	)

	// RequestDuration observes end-to-end request latency in seconds.
	RequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "gateway_request_duration_seconds",
			Help:    "End-to-end request duration in seconds.",
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
		},
		[]string{"provider", "model"},
	)

	// TokensInput counts total prompt tokens sent to providers.
	TokensInput = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_tokens_input_total",
			Help: "Total prompt tokens sent to providers.",
		},
		[]string{"provider", "model"},
	)

	// TokensOutput counts total completion tokens received from providers.
	TokensOutput = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_tokens_output_total",
			Help: "Total completion tokens received from providers.",
		},
		[]string{"provider", "model"},
	)

	// ProviderErrors counts errors broken down by provider and error type. The
	// vocabulary is the ErrType* constants, and every value but "plugin_error"
	// is produced by ProviderErrorType — see there for what each one means and
	// which of them are the provider's fault.
	//
	// Only "provider_error" and "timeout" describe an unhealthy upstream. Alert
	// on those two, not on the sum: "circuit_open", "backpressure" and
	// "client_canceled" are the gateway declining, shedding, or the caller
	// walking away, and the circuit breaker exonerates all three.
	ProviderErrors = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_provider_errors_total",
			Help: "Total provider errors by type.",
		},
		[]string{"provider", "error_type"},
	)

	// ProviderInitFailures counts providers whose factory failed at startup. A
	// failure is warned-and-skipped so one bad credential cannot stop the
	// gateway, which makes this counter the only machine-readable signal that a
	// configured provider is missing. Alert on any non-zero value.
	ProviderInitFailures = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_provider_init_failures_total",
			Help: "Total providers skipped because their factory failed at startup.",
		},
		[]string{"provider"},
	)

	// MCPServerInitFailures counts MCP servers whose initialize handshake or
	// tool discovery failed. A failure is logged and skipped so one unreachable
	// server cannot stop the gateway, which makes this counter the only
	// machine-readable signal that a configured MCP server never came up.
	// Alert on any non-zero value.
	MCPServerInitFailures = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_mcp_server_init_failures_total",
			Help: "Total MCP server initializations that failed.",
		},
		[]string{"server_name"},
	)

	// MCPServerUp tracks per-MCP-server availability as a gauge:
	// 0 = not ready (never initialized, or its transport has since died),
	// 1 = ready and advertising tools. A server that drops from 1 to 0 without
	// a config change has lost its transport — for stdio, its subprocess died.
	MCPServerUp = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gateway_mcp_server_up",
			Help: "MCP server availability (0=not ready 1=ready).",
		},
		[]string{"server_name"},
	)

	// RateLimitRejections counts requests rejected by rate limiting, labelled by
	// which limiter rejected them:
	//
	//   "ip"            general per-IP HTTP middleware
	//   "admin_session" the session-exchange route's own per-IP limiter, kept
	//                   separate so a burst of failed sign-ins is not lost in
	//                   ordinary traffic
	//   "plugin"        the rate-limit plugin, covering its global, per-key and
	//                   per-user buckets alike
	RateLimitRejections = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_rate_limit_rejections_total",
			Help: "Total requests rejected by rate limiting.",
		},
		[]string{"key_type"},
	)

	// RequestCostUSD tracks the estimated cumulative cost of requests in USD,
	// labelled by provider and model. Uses public pricing tables; actual costs
	// may differ.
	RequestCostUSD = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_request_cost_usd_total",
			Help: "Estimated total cost of requests in USD (based on public pricing tables).",
		},
		[]string{"provider", "model"},
	)

	// ServerConnectionsCurrent gauges current inbound HTTP connections, labelled
	// by connection state ("active", "idle").
	ServerConnectionsCurrent = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gateway_server_connections_current",
			Help: "Current inbound HTTP connections by state.",
		},
		[]string{"state"},
	)

	// ServerConnectionTransitionsTotal counts inbound HTTP connection state
	// transitions, labelled by the same state values emitted by
	// internal/httpserver's connStateLabel (e.g. "new", "active", "idle",
	// "hijacked", "closed", "unknown").
	ServerConnectionTransitionsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_server_connection_transitions_total",
			Help: "Total inbound HTTP connection state transitions.",
		},
		[]string{"state"},
	)

	// HookEventsDroppedTotal counts hook dispatches dropped because the hook
	// worker queue was full, labelled by hook subject.
	HookEventsDroppedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_hook_events_dropped_total",
			Help: "Total hook dispatches dropped because the hook worker queue was full.",
		},
		[]string{"subject"},
	)

	// ObservabilityEventsDroppedTotal counts observability events dropped
	// because the exporter dispatch queue was full, labelled by event subject.
	ObservabilityEventsDroppedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_observability_events_dropped_total",
			Help: "Total observability events dropped because the exporter dispatch queue was full.",
		},
		[]string{"subject"},
	)

	// CatalogLoadsTotal counts model catalog load attempts, labelled by source
	// ("remote", "fallback") and result ("success", "error").
	CatalogLoadsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_catalog_loads_total",
			Help: "Total model catalog load attempts by source and result.",
		},
		[]string{"source", "result"},
	)
)

// RequestMetricHandles stores cached Prometheus handles for a provider/model
// pair so hot-path metric updates avoid repeated vector lookups.
type RequestMetricHandles struct {
	Success   prometheus.Counter
	Error     prometheus.Counter
	Rejected  prometheus.Counter
	Duration  prometheus.Observer
	TokensIn  prometheus.Counter
	TokensOut prometheus.Counter
	CostUSD   prometheus.Counter
}

var (
	requestMetricCache sync.Map
	providerErrorCache sync.Map
)

// ForRequest returns cached metric handles for a provider/model pair. An empty
// provider is recorded as NoProviderLabel.
func ForRequest(provider, model string) *RequestMetricHandles {
	provider = providerLabel(provider)
	key := provider + "\x00" + model
	if cached, ok := requestMetricCache.Load(key); ok {
		return cached.(*RequestMetricHandles)
	}

	handles := &RequestMetricHandles{
		Success:   mustGetCounter(RequestsTotal, provider, model, "success"),
		Error:     mustGetCounter(RequestsTotal, provider, model, "error"),
		Rejected:  mustGetCounter(RequestsTotal, provider, model, "rejected"),
		Duration:  mustGetObserver(RequestDuration, provider, model),
		TokensIn:  mustGetCounter(TokensInput, provider, model),
		TokensOut: mustGetCounter(TokensOutput, provider, model),
		CostUSD:   mustGetCounter(RequestCostUSD, provider, model),
	}
	actual, _ := requestMetricCache.LoadOrStore(key, handles)
	return actual.(*RequestMetricHandles)
}

// error_type label values on ProviderErrors.
const (
	// ErrTypeProvider is an upstream that answered badly: the provider's fault.
	ErrTypeProvider = "provider_error"
	// ErrTypeCircuitOpen is the gateway declining to call a target at all.
	// A refused call and a failed one are different incidents: an open circuit
	// means no request was made, so counting it as a provider error would report
	// an outage the provider may already have recovered from.
	ErrTypeCircuitOpen = "circuit_open"
	// ErrTypeTimeout is a bound the GATEWAY imposed on the upstream elapsing:
	// the provider was too slow to answer, which is its fault. Reported apart
	// from ErrTypeProvider because "answered nothing at all" and "answered
	// badly" are diagnosed in different places.
	ErrTypeTimeout = "timeout"
	// ErrTypeClientCanceled is the caller going away, or the caller's own
	// deadline elapsing. Nothing upstream is at fault.
	ErrTypeClientCanceled = "client_canceled"
	// ErrTypeBackpressure is the gateway shedding load under its own
	// per-target concurrency limit (targets[].concurrency): a 429 the provider
	// never saw. It is per-provider because "which target is saturated" is the
	// question it answers — but it is capacity, not health.
	ErrTypeBackpressure = "backpressure"
	// ErrTypePlugin is an after_request plugin failing on a request the
	// provider had already served.
	ErrTypePlugin = "plugin_error"
)

// ProviderErrorType is the ONE classifier behind the error_type label. Every
// routed surface — chat, streaming, embeddings, images — resolves the label
// here, so the same failure can never land under two different labels depending
// on which endpoint the caller used.
//
// It answers exactly the question the circuit breaker answers (whose fault was
// this), which is why the breaker is built on it: a metric that convicts a
// provider the breaker simultaneously exonerates is worse than no metric.
//
// ctx supplies the facts the error alone cannot carry. Whether a provider
// propagates context.Canceled out of a cancelled call is a property of that
// provider's SDK — one that returns its own "request aborted" or a bare
// io.ErrUnexpectedEOF drops it — so a classifier reading only the error blames
// the provider for the client hanging up. A nil or never-cancelled ctx is
// allowed and simply contributes nothing.
func ProviderErrorType(ctx context.Context, err error) string {
	switch {
	case errors.Is(err, circuitbreaker.ErrCircuitOpen):
		return ErrTypeCircuitOpen
	case errors.Is(err, core.ErrProviderSaturated):
		return ErrTypeBackpressure
	}

	// context.Cause names WHOSE bound ended the request. The stdlib returns one
	// of its own two sentinels BY IDENTITY when nobody named a reason, so a
	// cause that is anything else is one this gateway attached — and every cause
	// it attaches is a deadline it imposed on the upstream (ErrRequestTimeout,
	// streamio.ErrIdleTimeout).
	//
	// Compared by identity, not errors.Is, and that is load-bearing: those two
	// sentinels WRAP context.DeadlineExceeded precisely so packages that cannot
	// import them can still answer "did this time out", so errors.Is would read
	// the gateway's own deadline as the caller's.
	if ctx != nil {
		switch cause := context.Cause(ctx); {
		//nolint:errorlint // identity is the question: errors.Is here would match
		// the gateway's own deadline sentinels, which wrap context.DeadlineExceeded
		// on purpose, and file every stalled upstream under the caller's name.
		case cause == context.Canceled, cause == context.DeadlineExceeded:
			return ErrTypeClientCanceled
		case cause != nil:
			return ErrTypeTimeout
		}
	}

	// The context is still alive, so a deadline reported in the error is one the
	// provider's own HTTP client imposed — still a slow upstream.
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrTypeTimeout
	}
	return ErrTypeProvider
}

// ForProviderError returns a cached metric handle for a provider/error-type
// pair. An empty provider is recorded as NoProviderLabel.
func ForProviderError(provider, errType string) prometheus.Counter {
	provider = providerLabel(provider)
	key := provider + "\x00" + errType
	if cached, ok := providerErrorCache.Load(key); ok {
		return cached.(prometheus.Counter)
	}

	counter := mustGetCounter(ProviderErrors, provider, errType)
	actual, _ := providerErrorCache.LoadOrStore(key, counter)
	return actual.(prometheus.Counter)
}

func mustGetCounter(vec *prometheus.CounterVec, labels ...string) prometheus.Counter {
	counter, err := vec.GetMetricWithLabelValues(labels...)
	if err != nil {
		panic(err)
	}
	return counter
}

func mustGetObserver(vec *prometheus.HistogramVec, labels ...string) prometheus.Observer {
	observer, err := vec.GetMetricWithLabelValues(labels...)
	if err != nil {
		panic(err)
	}
	return observer
}
