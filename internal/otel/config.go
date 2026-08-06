package otel

import (
	"time"

	"github.com/ferro-labs/ai-gateway/internal/tracingpolicy"
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

	// SampleRatio is the head sampler ratio between 0.0 and 1.0. It is the only
	// source for the sampler: the standard OTEL_TRACES_SAMPLER and
	// OTEL_TRACES_SAMPLER_ARG variables are not consulted, so a deployment that
	// sets them alone samples at whatever this says.
	SampleRatio float64 `yaml:"sample_ratio" json:"sample_ratio"`

	// PrivacyLevel controls whether prompt/response content is exported.
	// One of: "none", "metadata" (default), "full".
	PrivacyLevel string `yaml:"privacy_level" json:"privacy_level"`

	// ShutdownGrace is the maximum time each shutdown stage will block waiting
	// for in-flight exports to drain. Exporter shutdown and TracerProvider
	// shutdown each receive this budget, so total shutdown may take up to twice
	// this value.
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
		ShutdownGrace: defaultShutdownGrace,
	}
}

// PrivacyLevel constants, re-exported from the internal tracingpolicy
// package — the single source of truth also used by config validation.
const (
	PrivacyLevelNone     = tracingpolicy.PrivacyLevelNone
	PrivacyLevelMetadata = tracingpolicy.PrivacyLevelMetadata
	PrivacyLevelFull     = tracingpolicy.PrivacyLevelFull
)

// Validate returns an error when PrivacyLevel is set to an unrecognised value
// or Endpoint cannot be understood. An empty string is accepted for both:
// PrivacyLevel is then the default ("metadata"), and an empty Endpoint means
// tracing is driven by the OTEL_EXPORTER_OTLP_* environment or not at all.
// Callers should invoke this before constructing a provider. The allowed
// privacy levels live in the internal tracingpolicy package so this validator
// and the gateway config validator share one source of truth.
//
// The endpoint is checked here, at validation, rather than left to fail at
// export time. An endpoint the exporter cannot use disables tracing completely
// while the gateway reports itself as tracing, and the only symptom is a
// per-batch error deep inside the SDK long after anyone was watching.
func (c Config) Validate() error {
	if err := tracingpolicy.ValidatePrivacyLevel(c.PrivacyLevel); err != nil {
		return err
	}
	_, err := parseConfiguredEndpoint(c.Endpoint, isHTTPProtocol(c.Protocol))
	return err
}

// effectiveEndpoint resolves whether a collector is configured, and which
// setting named it. The standard environment variables take precedence over the
// configured Endpoint — this matches the documented OTEL_* precedence and is
// required so containerised deployments can redirect telemetry by env var
// regardless of any baked-in config. The configured Endpoint is the fallback.
// An empty result means OTel is effectively disabled even when Enabled is true.
//
// Both standard variables count. OTEL_EXPORTER_OTLP_TRACES_ENDPOINT alone is a
// complete traces configuration under the specification, and a deployment that
// set only it used to get a NoOp provider — tracing off, with nothing said.
//
// The value returned here decides whether to build a pipeline, not where to
// send. Where to send is resolveExportTarget's job, and for both variables it
// hands the value to the SDK unread, because the two have different and precise
// path semantics that only the SDK implements correctly.
func (c Config) effectiveEndpoint(getenv func(string) string) string {
	if v := getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"); v != "" {
		return v
	}
	if v := getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); v != "" {
		return v
	}
	return c.Endpoint
}
