// Package metrics registers the Prometheus metrics used by the gateway.
// Import this package (via blank import) from the server entry point to
// register all metrics before the /metrics handler is mounted.
package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

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

	// ProviderErrors counts errors broken down by provider and error type
	// ("provider_error", "circuit_open", "timeout").
	ProviderErrors = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_provider_errors_total",
			Help: "Total provider errors by type.",
		},
		[]string{"provider", "error_type"},
	)

	// CircuitBreakerState tracks per-provider circuit breaker state as a gauge:
	// 0 = closed, 1 = open, 2 = half_open.
	CircuitBreakerState = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gateway_circuit_breaker_state",
			Help: "Circuit breaker state per provider (0=closed 1=open 2=half_open).",
		},
		[]string{"provider"},
	)

	// RateLimitRejections counts requests rejected by the rate-limit middleware
	// or plugin, labelled by key_type ("ip", "api_key", "plugin").
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

	ServerConnectionsCurrent = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gateway_server_connections_current",
			Help: "Current inbound HTTP connections by state.",
		},
		[]string{"state"},
	)

	ServerConnectionTransitionsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_server_connection_transitions_total",
			Help: "Total inbound HTTP connection state transitions.",
		},
		[]string{"state"},
	)

	HookEventsDroppedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_hook_events_dropped_total",
			Help: "Total hook dispatches dropped because the hook worker queue was full.",
		},
		[]string{"subject"},
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

// ForRequest returns cached metric handles for a provider/model pair.
func ForRequest(provider, model string) *RequestMetricHandles {
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

// ForProviderError returns a cached metric handle for a provider/error-type pair.
func ForProviderError(provider, errType string) prometheus.Counter {
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
