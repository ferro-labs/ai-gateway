package metrics

import (
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
)

// Circuit-breaker states as encoded by gateway_circuit_breaker_state.
const (
	CircuitClosed   = 0
	CircuitOpen     = 1
	CircuitHalfOpen = 2
)

// CircuitBreakerStateSource reports the current state of every circuit breaker
// the process has configured, keyed by provider name and valued with one of the
// Circuit* constants above.
//
// It must be safe for concurrent use and must not block: it is called on every
// Prometheus scrape.
type CircuitBreakerStateSource func() map[string]float64

// circuitBreakerSource is the installed source, or nil before one is installed.
// A pointer is stored because a func value is not comparable and so cannot be
// held in an atomic.Value directly.
var circuitBreakerSource atomic.Pointer[CircuitBreakerStateSource]

// SetCircuitBreakerStateSource installs the source that gateway_circuit_breaker_state
// is resolved from at scrape time. Passing nil removes it, after which the
// metric reports nothing.
//
// The gauge is *derived*, never pushed, and that is the whole point. A breaker
// leaves Open on a timer rather than on an event, so a gauge written only when a
// request completes reports "open" for the entire recovery window — and forever
// once traffic stops, since nothing is left to write it. An alert on
// circuit_breaker_state == 1 could then never clear itself. Reading the live
// breakers on each scrape makes that state unrepresentable rather than merely
// unlikely, which is also the reasoning behind Prometheus's own "timestamps, not
// time since" rule: export what can be read now, so no update path can get stuck
// (https://prometheus.io/docs/practices/instrumentation/#timestamps-not-time-since).
//
// Exactly one source is held. A process runs one gateway; a second Set replaces
// the first rather than merging, so a caller that builds a replacement gateway
// installs its source and the retired one stops being read.
func SetCircuitBreakerStateSource(fn CircuitBreakerStateSource) {
	if fn == nil {
		circuitBreakerSource.Store(nil)
		return
	}
	circuitBreakerSource.Store(&fn)
}

// circuitBreakerStateDesc describes gateway_circuit_breaker_state.
//
// Presence is meaningful: the source reports every provider that has a breaker
// configured, from process start and whether or not it has ever served a
// request, so an absent series means no breaker is configured for that provider
// rather than one that has not been exercised yet. That follows Prometheus's
// "avoid missing metrics" guidance — "export a default value such as 0 for any
// time series you know may exist in advance"
// (https://prometheus.io/docs/practices/instrumentation/#avoid-missing-metrics).
//
// the state stays encoded as a number on one series. OpenMetrics
// would model it as a StateSet — one series per state, exactly one carrying 1 —
// but that renames every series and breaks existing alerts for a distinction
// this metric does not yet need.
var circuitBreakerStateDesc = prometheus.NewDesc(
	"gateway_circuit_breaker_state",
	"Circuit breaker state per provider (0=closed 1=open 2=half_open). "+
		"Reported for every provider with a breaker configured; an absent series means no breaker is configured.",
	[]string{"provider"},
	nil,
)

// circuitBreakerCollector resolves gateway_circuit_breaker_state from the live
// breakers each time the registry gathers.
type circuitBreakerCollector struct{}

func (circuitBreakerCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- circuitBreakerStateDesc
}

func (circuitBreakerCollector) Collect(ch chan<- prometheus.Metric) {
	src := circuitBreakerSource.Load()
	if src == nil {
		return
	}
	for provider, state := range (*src)() {
		ch <- prometheus.MustNewConstMetric(circuitBreakerStateDesc, prometheus.GaugeValue, state, provider)
	}
}

func init() {
	prometheus.MustRegister(circuitBreakerCollector{})
}
