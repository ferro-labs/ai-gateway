package transport

import "github.com/prometheus/client_golang/prometheus"

// Metrics holds Prometheus metrics for transport observability.
// Critical for diagnosing connection pool saturation in production.
type Metrics struct {
	ConnectionsActive *prometheus.GaugeVec
	ConnectionErrors  *prometheus.CounterVec
	RequestDuration   *prometheus.HistogramVec
}

func newMetrics() *Metrics {
	return &Metrics{
		ConnectionsActive: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "ferro",
				Subsystem: "transport",
				Name:      "connections_active",
				Help:      "Active upstream connections per provider",
			},
			[]string{"provider"},
		),
		ConnectionErrors: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "ferro",
				Subsystem: "transport",
				Name:      "connection_errors_total",
				Help:      "Total upstream connection errors per provider",
			},
			[]string{"provider", "error_type"},
		),
		RequestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "ferro",
				Subsystem: "transport",
				Name:      "request_duration_seconds",
				Help:      "Upstream request duration per provider",
				Buckets: []float64{
					.001, .005, .01, .025, .05,
					.1, .25, .5, 1, 2.5, 5, 10,
				},
			},
			[]string{"provider", "status"},
		),
	}
}

// Register registers all metrics with a Prometheus registerer.
func (m *Metrics) Register(reg prometheus.Registerer) error {
	for _, c := range []prometheus.Collector{
		m.ConnectionsActive,
		m.ConnectionErrors,
		m.RequestDuration,
	} {
		if err := reg.Register(c); err != nil {
			return err
		}
	}
	return nil
}
