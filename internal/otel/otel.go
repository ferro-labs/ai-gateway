package otel

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/ferro-labs/ai-gateway/observability"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// ShutdownFunc is returned by Init. Callers MUST invoke it with a
// deadline-bounded context during graceful shutdown.
type ShutdownFunc func(ctx context.Context) error

// Init constructs an observability.Provider. Returns observability.NoOp()
// (zero-allocation fast-path) when:
//   - cfg.Enabled is false, OR
//   - cfg.effectiveEndpoint() is empty (no OTEL_EXPORTER_OTLP_ENDPOINT
//     env var and no cfg.Endpoint) AND no enabled exporter is configured.
//
// In all other cases the returned Provider is the OTel-backed
// implementation. When an OTLP endpoint is configured, a real
// TracerProvider with an OTLP span exporter is built. When only plugin
// exporters are configured (no endpoint), a no-op TracerProvider is used
// for spans so RecordEvent still fans events out to the exporters without
// requiring a live OTLP collector.
//
// The ShutdownFunc drains in-flight exports within the supplied
// context deadline. Issue #49 acceptance criterion: when neither an OTLP
// endpoint nor any enabled exporter is configured, no extra goroutines are
// started and no allocations occur on the hot path.
func Init(ctx context.Context, cfg Config) (observability.Provider, ShutdownFunc, error) {
	if err := cfg.Validate(); err != nil {
		return nil, nil, fmt.Errorf("otel: invalid config: %w", err)
	}

	hasEndpoint := cfg.effectiveEndpoint(os.Getenv) != ""

	// Count enabled plugin exporters.
	enabledExporterCount := 0
	for _, e := range cfg.Exporters {
		if e.Enabled {
			enabledExporterCount++
		}
	}

	// Fast-path: nothing configured → zero-alloc NoOp.
	if !cfg.Enabled || (!hasEndpoint && enabledExporterCount == 0) {
		return observability.NoOp(), noopShutdown, nil
	}

	var prov *otelProvider
	var tpShutdown func(context.Context) error
	globalTPSet := false

	if hasEndpoint {
		// Full OTLP pipeline: real TracerProvider + span exporter.
		exporter, err := newSpanExporter(ctx, cfg)
		if err != nil {
			return nil, nil, fmt.Errorf("otel: build span exporter: %w", err)
		}

		res, err := resource.New(ctx,
			resource.WithAttributes(
				semconv.ServiceName(serviceName(cfg)),
				semconv.ServiceVersion(""), // populated later via build flag
			),
			resource.WithFromEnv(),
			resource.WithProcess(),
			resource.WithHost(),
		)
		if err != nil {
			return nil, nil, fmt.Errorf("otel: build resource: %w", err)
		}

		tp := sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(exporter),
			sdktrace.WithResource(res),
			sdktrace.WithSampler(sampler(cfg)),
			sdktrace.WithIDGenerator(newLoggingIDGen()),
		)

		installPropagator()

		// Register tp as the global TracerProvider so instrumentation that
		// uses the global otel.Tracer(...) API — plugin-stage spans
		// (plugin/manager.go) and MCP tool spans (internal/mcp/executor.go) —
		// records and exports child spans. Without this they silently no-op
		// and only the gateway root span (which holds tp directly) is emitted.
		otel.SetTracerProvider(tp)
		globalTPSet = true

		prov = newProvider(trace.TracerProvider(tp), cfg)
		tpShutdown = tp.Shutdown
	} else {
		// Exporters-only path: no-op tracer so spans are free, but
		// RecordEvent still fans events out to registered exporters.
		noopTP := noop.NewTracerProvider()
		prov = newProvider(trace.TracerProvider(noopTP), cfg)
		tpShutdown = noopShutdown
	}

	// Resolve plugin exporters: look up factory, instantiate, Init.
	resolvedExporters := resolveExporters(ctx, cfg.Exporters)
	if len(resolvedExporters) > 0 {
		prov.AttachExporters(resolvedExporters)
	}

	shutdownGrace := cfg.ShutdownGrace
	if shutdownGrace <= 0 {
		shutdownGrace = 10 * time.Second
	}
	shutdown := func(ctx context.Context) error {
		// Apply an internal deadline so callers can pass context.Background()
		// and the closure still drains within ShutdownGrace.
		innerCtx, cancel := context.WithTimeout(ctx, shutdownGrace)
		defer cancel()
		// Drain plugin exporters first, then the OTel pipeline.
		exporterErr := prov.Shutdown(innerCtx)
		tpErr := tpShutdown(innerCtx)
		// Restore the global TracerProvider to a no-op so any late
		// otel.Tracer(...) calls after shutdown don't hit the drained
		// pipeline, and so re-initialisation (tests, embedders) starts clean.
		if globalTPSet {
			otel.SetTracerProvider(noop.NewTracerProvider())
		}
		if tpErr != nil {
			return tpErr
		}
		return exporterErr
	}

	return prov, shutdown, nil
}

// resolveExporters instantiates and initialises each enabled exporter.
// Unknown names and Init errors are warned and skipped so a misconfigured
// optional plugin cannot prevent the gateway from starting.
func resolveExporters(ctx context.Context, cfgs []ExporterConfig) []observability.Exporter {
	out := make([]observability.Exporter, 0, len(cfgs))
	for _, ec := range cfgs {
		if !ec.Enabled {
			continue
		}
		factory, ok := observability.LookupExporter(ec.Name)
		if !ok {
			logging.Logger.Warn("otel: exporter not registered; skipping",
				"name", ec.Name,
			)
			continue
		}
		ex := factory()
		if err := ex.Init(ctx, ec.Config); err != nil {
			logging.Logger.Warn("otel: exporter Init failed; skipping",
				"name", ec.Name,
				"error", err,
			)
			continue
		}
		out = append(out, ex)
	}
	return out
}

// newSpanExporter constructs the OTLP span exporter for the configured
// protocol. Defaults to gRPC.
//
// Transport security is derived from the endpoint scheme:
//   - https:// → TLS (WithInsecure omitted; the exporter defaults to secure)
//   - http://  → plaintext (WithInsecure applied)
//   - bare host:port → plaintext for backward compatibility (WithInsecure applied)
func newSpanExporter(ctx context.Context, cfg Config) (sdktrace.SpanExporter, error) {
	raw := cfg.effectiveEndpoint(os.Getenv)
	insecure := !endpointIsSecure(raw)
	host := stripScheme(raw)

	h := resolveHeaders(cfg.Headers)

	switch cfg.Protocol {
	case "http/protobuf", "http":
		opts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(host)}
		if insecure {
			opts = append(opts, otlptracehttp.WithInsecure())
		}
		if len(h) > 0 {
			opts = append(opts, otlptracehttp.WithHeaders(h))
		}
		return otlptrace.New(ctx, otlptracehttp.NewClient(opts...))
	default:
		opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(host)}
		if insecure {
			opts = append(opts, otlptracegrpc.WithInsecure())
		}
		if len(h) > 0 {
			opts = append(opts, otlptracegrpc.WithHeaders(h))
		}
		return otlptrace.New(ctx, otlptracegrpc.NewClient(opts...))
	}
}

// resolveHeaders materialises OTLP export headers from the configuration map.
// Each value is expanded via os.Expand so both $VAR and ${VAR} references are
// substituted with the corresponding environment variable. Headers whose
// resolved value is empty (e.g. they referenced an unset env var) are omitted
// and a warning is logged — sending an empty header value to the backend is
// almost never intentional and may cause authentication failures. Literal
// (non-$) values pass through unchanged.
func resolveHeaders(raw map[string]string) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		resolved := os.Expand(v, os.Getenv)
		if resolved == "" {
			logging.Logger.Warn("otel: header resolved to empty; skipping", "header", k)
			continue
		}
		out[k] = resolved
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// endpointIsSecure reports whether the endpoint explicitly uses the https://
// scheme. Both http:// and bare host:port are treated as insecure.
func endpointIsSecure(endpoint string) bool {
	return strings.HasPrefix(endpoint, "https://")
}

// sampler returns a head sampler matching cfg.SampleRatio. 1.0
// (default) becomes AlwaysSample for a small allocation win.
func sampler(cfg Config) sdktrace.Sampler {
	if cfg.SampleRatio >= 1.0 {
		return sdktrace.AlwaysSample()
	}
	if cfg.SampleRatio <= 0 {
		return sdktrace.NeverSample()
	}
	return sdktrace.TraceIDRatioBased(cfg.SampleRatio)
}

// serviceName returns the configured service.name, defaulting to
// "ferrogw" when unset.
func serviceName(cfg Config) string {
	if cfg.ServiceName != "" {
		return cfg.ServiceName
	}
	return "ferrogw"
}

// stripScheme removes a leading http:// or https:// from an endpoint
// since the OTLP exporters expect host:port form.
func stripScheme(endpoint string) string {
	for _, prefix := range []string{"https://", "http://"} {
		if len(endpoint) > len(prefix) && endpoint[:len(prefix)] == prefix {
			return endpoint[len(prefix):]
		}
	}
	return endpoint
}

// noopShutdown is the placeholder Shutdown function returned alongside
// a NoOp Provider.
func noopShutdown(_ context.Context) error { return nil }
