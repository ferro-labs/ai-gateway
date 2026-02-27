// Package metrics registers the Prometheus metrics used by the gateway.
// Import this package (via blank import) from the server entry point to
// register all metrics before the /metrics handler is mounted.
package metrics

import (
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
)
