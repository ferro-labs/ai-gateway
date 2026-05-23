package otel

import (
	"fmt"
	"time"
)

// Config controls OpenTelemetry behaviour. The gateway exposes this as
// observability.tracing.* in config.yaml. Standard OTEL_* env vars
// always take precedence (matches the OTel SDK convention and is
// required for predictable container deployments).
type Config struct {
	// Enabled is the master switch. When false, Init returns a NoOp
	// Provider regardless of other settings.
	Enabled bool `yaml:"enabled" json:"enabled"`

	// Endpoint overrides OTEL_EXPORTER_OTLP_ENDPOINT. When both are
	// empty and no exporters are configured, Init falls back to NoOp.
	Endpoint string `yaml:"endpoint" json:"endpoint"`

	// Protocol selects the OTLP transport: "grpc" or "http/protobuf".
	Protocol string `yaml:"protocol" json:"protocol"`

	// ServiceName sets the service.name OTel resource attribute.
	ServiceName string `yaml:"service_name" json:"service_name"`

	// SampleRatio is the head sampler ratio between 0.0 and 1.0.
	// Overridden by OTEL_TRACES_SAMPLER + OTEL_TRACES_SAMPLER_ARG.
	SampleRatio float64 `yaml:"sample_ratio" json:"sample_ratio"`

	// PrivacyLevel controls whether prompt/response content is exported.
	// One of: "none", "metadata" (default), "full".
	PrivacyLevel string `yaml:"privacy_level" json:"privacy_level"`

	// ShutdownGrace is the maximum time Shutdown will block waiting for
	// in-flight exports to drain.
	ShutdownGrace time.Duration `yaml:"shutdown_grace" json:"shutdown_grace"`

	// Exporters is the list of plugin exporter configurations.  Each
	// entry names a factory registered via observability.RegisterExporter.
	// Unrecognised names are warned and skipped — they do not cause Init
	// to fail.  This field is populated by internal/bootstrap from the
	// public ObservabilityConfig.Exporters slice.
	Exporters []ExporterConfig `yaml:"exporters" json:"exporters"`

	// Headers are additional HTTP/gRPC metadata headers sent with every OTLP
	// export request. Values may contain ${ENV_VAR} references that are
	// resolved at exporter-build time (not at config-load time) so that
	// secrets are never stored literally in the in-memory config.
	Headers map[string]string `yaml:"headers" json:"headers"`
}

// ExporterConfig is the internal mirror of the public ExporterConfig.
// It is kept here so the internal/otel package does not import the root
// aigateway package (which would create an import cycle).
type ExporterConfig struct {
	// Name is the canonical exporter name used to look up the factory.
	Name string `yaml:"name" json:"name"`
	// Enabled gates the exporter.
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Config is passed verbatim to Exporter.Init.
	Config map[string]any `yaml:"config" json:"config"`
}

// DefaultConfig returns the production-safe defaults: enabled, gRPC
// OTLP, "metadata" privacy, 10 second shutdown grace.
func DefaultConfig() Config {
	return Config{
		Enabled:       true,
		Protocol:      "grpc",
		ServiceName:   "ferrogw",
		SampleRatio:   1.0,
		PrivacyLevel:  PrivacyLevelMetadata,
		ShutdownGrace: 10 * time.Second,
	}
}

// PrivacyLevel constants.
const (
	PrivacyLevelNone     = "none"
	PrivacyLevelMetadata = "metadata"
	PrivacyLevelFull     = "full"
)

// Validate returns an error when PrivacyLevel is set to an unrecognised
// value. An empty string is accepted and treated as the default
// ("metadata") by the provider. Callers should invoke this before
// constructing a provider.
//
// NOTE: the same allowed set is mirrored in config_load.go ValidateConfig;
// keep both in sync.
func (c Config) Validate() error {
	switch c.PrivacyLevel {
	case "", PrivacyLevelNone, PrivacyLevelMetadata, PrivacyLevelFull:
		return nil
	default:
		return fmt.Errorf("invalid privacy_level %q: must be one of none, metadata, full", c.PrivacyLevel)
	}
}

// effectiveEndpoint resolves the OTLP endpoint. The standard
// OTEL_EXPORTER_OTLP_ENDPOINT environment variable takes precedence over the
// configured Endpoint — this matches the documented OTEL_* precedence and is
// required so containerised deployments can redirect telemetry by env var
// regardless of any baked-in config. The configured Endpoint is the fallback.
// An empty result means OTel is effectively disabled even when Enabled is true.
func (c Config) effectiveEndpoint(getenv func(string) string) string {
	if v := getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); v != "" {
		return v
	}
	return c.Endpoint
}
