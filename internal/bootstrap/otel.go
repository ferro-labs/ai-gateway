package bootstrap

import (
	"os"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/logging"
	gwotel "github.com/ferro-labs/ai-gateway/internal/otel"
)

// otelConfigFromGateway translates the public gateway ObservabilityConfig
// into the internal gwotel.Config consumed by gwotel.Init.
//
// Standard OTEL_* environment variables take precedence over config:
// OTEL_EXPORTER_OTLP_ENDPOINT overrides (and can activate) tracing even when
// the config endpoint is blank, matching the documented precedence for
// predictable container deployments. DefaultConfig supplies production-safe
// fallbacks for any fields the user leaves blank.
//
// The provider is activated when an OTLP endpoint is set (via config OR
// OTEL_EXPORTER_OTLP_ENDPOINT) OR at least one enabled exporter is listed.
func otelConfigFromGateway(obs aigateway.ObservabilityConfig) gwotel.Config {
	t := obs.Tracing
	cfg := gwotel.DefaultConfig()

	// Count enabled exporters so they can activate the provider even without
	// an OTLP endpoint.
	enabledExporters := 0
	for _, e := range obs.Exporters {
		if e.Enabled {
			enabledExporters++
		}
	}

	// The public Enabled defaults to false (Go zero value); only honour
	// it as a hard "off" switch. When the user did not set it but provided
	// an endpoint (config OR OTEL_EXPORTER_OTLP_ENDPOINT) or enabled
	// exporters, we treat the section as opting in.
	envEndpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	cfg.Enabled = t.Enabled || t.Endpoint != "" || envEndpoint != "" || enabledExporters > 0
	if t.Endpoint != "" {
		cfg.Endpoint = t.Endpoint
	}
	if t.Protocol != "" {
		cfg.Protocol = t.Protocol
	}
	if t.ServiceName != "" {
		cfg.ServiceName = t.ServiceName
	}
	// Honour an explicit sample_ratio, including 0.0 (sample nothing). A nil
	// pointer means the field was omitted, so the DefaultConfig value stands.
	if t.SampleRatio != nil {
		cfg.SampleRatio = *t.SampleRatio
	}
	if t.PrivacyLevel != "" {
		cfg.PrivacyLevel = t.PrivacyLevel
	}
	if t.ShutdownGrace != "" {
		if d, err := time.ParseDuration(t.ShutdownGrace); err == nil {
			cfg.ShutdownGrace = d
		} else {
			logging.Logger.Warn("otel: invalid shutdown_grace duration; using default",
				"value", t.ShutdownGrace,
				"error", err,
				"default", cfg.ShutdownGrace,
			)
		}
	}

	// Map public ExporterConfig entries to internal ones.
	if len(obs.Exporters) > 0 {
		cfg.Exporters = make([]gwotel.ExporterConfig, 0, len(obs.Exporters))
		for _, e := range obs.Exporters {
			cfg.Exporters = append(cfg.Exporters, gwotel.ExporterConfig{
				Name:    e.Name,
				Enabled: e.Enabled,
				Config:  e.Config,
			})
		}
	}

	// Copy OTLP export headers. Values may contain ${ENV_VAR} references
	// that are resolved lazily at exporter-build time by internal/otel.
	if len(t.Headers) > 0 {
		cfg.Headers = t.Headers
	}

	return cfg
}
