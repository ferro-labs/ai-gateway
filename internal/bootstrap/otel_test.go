package bootstrap

import (
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
)

func float64Ptr(v float64) *float64 { return &v }

func TestOtelConfigFromGateway_EnvEndpointActivates(t *testing.T) {
	// An OTEL_EXPORTER_OTLP_ENDPOINT alone (no config endpoint, Enabled unset)
	// must opt the tracing section in, matching the documented env precedence.
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317")

	cfg := otelConfigFromGateway(aigateway.ObservabilityConfig{})
	if !cfg.Enabled {
		t.Error("env endpoint should activate tracing (cfg.Enabled = true)")
	}
}

func TestOtelConfigFromGateway_NothingConfigured_Disabled(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	cfg := otelConfigFromGateway(aigateway.ObservabilityConfig{})
	if cfg.Enabled {
		t.Error("with no endpoint, env, or exporters, tracing must stay disabled")
	}
}

func TestOtelConfigFromGateway_SampleRatio(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	tests := []struct {
		name string
		in   *float64
		want float64
	}{
		{"omitted falls back to default", nil, 1.0},
		{"explicit zero disables sampling", float64Ptr(0.0), 0.0},
		{"explicit fraction is honoured", float64Ptr(0.25), 0.25},
		{"explicit one is honoured", float64Ptr(1.0), 1.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obs := aigateway.ObservabilityConfig{
				Tracing: aigateway.TracingConfig{
					Endpoint:    "localhost:4317",
					SampleRatio: tt.in,
				},
			}
			cfg := otelConfigFromGateway(obs)
			if cfg.SampleRatio != tt.want {
				t.Errorf("SampleRatio = %v, want %v", cfg.SampleRatio, tt.want)
			}
		})
	}
}
