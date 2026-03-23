package metrics

import (
	"github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/prometheus/client_golang/prometheus"
)

func init() {
	// Register transport-level metrics with the default Prometheus registerer.
	// Errors are ignored for idempotent registration (metrics may already exist
	// if the package is imported multiple times in tests).
	_ = httpclient.Manager().Metrics().Register(prometheus.DefaultRegisterer)
}
