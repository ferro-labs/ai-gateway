package otel

import (
	"context"
	"errors"
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

// defaultShutdownGrace bounds each shutdown stage when ShutdownGrace is unset.
const defaultShutdownGrace = 10 * time.Second

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

	prov, tpShutdown, installedTP, err := buildOTLPProvider(ctx, cfg, hasEndpoint)
	if err != nil {
		return nil, nil, err
	}

	// Publish the provider's privacy level and redactor so child spans created
	// from the global tracer (plugin stages, MCP tool calls) redact error text
	// with the same policy as the gateway root span.
	setSpanErrorPolicy(prov.privacyLevel, prov.redactor)

	// Resolve plugin exporters: look up factory, instantiate, Init.
	resolvedExporters := resolveExporters(ctx, cfg.Exporters)
	if len(resolvedExporters) > 0 {
		prov.AttachExporters(resolvedExporters)
	}

	return prov, makeShutdown(prov, tpShutdown, cfg.ShutdownGrace, installedTP), nil
}

// buildOTLPProvider constructs the otelProvider and its TracerProvider shutdown
// function. With an OTLP endpoint it builds a real TracerProvider plus OTLP
// span exporter and installs it as the global TracerProvider (so child spans
// from the global tracer are exported), returning the installed instance so
// the caller can later verify ownership before resetting the global.
// Otherwise it returns a provider backed by a no-op TracerProvider so spans
// are free while RecordEvent still fans events out to exporters, and a nil
// installed instance since no global was set.
func buildOTLPProvider(ctx context.Context, cfg Config, hasEndpoint bool) (*otelProvider, func(context.Context) error, trace.TracerProvider, error) {
	if !hasEndpoint {
		// Exporters-only path: no-op tracer so spans are free, but
		// RecordEvent still fans events out to registered exporters.
		noopTP := noop.NewTracerProvider()
		return newProvider(trace.TracerProvider(noopTP), cfg), noopShutdown, nil, nil
	}

	// Full OTLP pipeline: real TracerProvider + span exporter.
	exporter, err := newSpanExporter(ctx, cfg)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("otel: build span exporter: %w", err)
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
		// The exporter already opened its transport (e.g. a gRPC/HTTP
		// connection); shut it down before returning so this failure path
		// doesn't leak it on every init retry. Use a bounded context —
		// distinct from the original err — and don't let a shutdown
		// failure mask the primary resource.New error.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), defaultShutdownGrace)
		if shutdownErr := exporter.Shutdown(shutdownCtx); shutdownErr != nil {
			logging.Logger.Warn("otel: span exporter shutdown after resource build failure",
				"error", shutdownErr,
			)
		}
		cancel()
		return nil, nil, nil, fmt.Errorf("otel: build resource: %w", err)
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

	installedTP := trace.TracerProvider(tp)
	return newProvider(installedTP, cfg), tp.Shutdown, installedTP, nil
}

// makeShutdown builds the ShutdownFunc that drains plugin exporters and the
// OTel pipeline within grace, then restores the global TracerProvider to a
// no-op — but only when the global TracerProvider is still the exact
// instance this Init call installed. If a later Init call (e.g. a config
// reload) has since replaced it, resetting here would silently disable that
// newer, still-active provider's tracing, so the reset is skipped and only
// this Init's own owned resources (exporter, tp) are shut down.
func makeShutdown(prov *otelProvider, tpShutdown func(context.Context) error, grace time.Duration, installedTP trace.TracerProvider) ShutdownFunc {
	if grace <= 0 {
		grace = defaultShutdownGrace
	}
	return func(ctx context.Context) error {
		// Drain plugin exporters first, then the OTel pipeline.
		err := shutdownWithIndependentDeadlines(ctx, grace, prov.Shutdown, tpShutdown)
		// Restore the global TracerProvider to a no-op so any late
		// otel.Tracer(...) calls after shutdown don't hit the drained
		// pipeline, and so re-initialisation (tests, embedders) starts clean —
		// but only if no later Init call has since replaced the global.
		if installedTP != nil && otel.GetTracerProvider() == installedTP {
			otel.SetTracerProvider(noop.NewTracerProvider())
		}
		return err
	}
}

func shutdownWithIndependentDeadlines(
	ctx context.Context,
	shutdownGrace time.Duration,
	exporterShutdown func(context.Context) error,
	tpShutdown func(context.Context) error,
) error {
	if shutdownGrace <= 0 {
		shutdownGrace = defaultShutdownGrace
	}

	// Give each shutdown stage its own grace window. A slow plugin exporter
	// may use its full deadline, but that must not hand the TracerProvider an
	// already-expired context and silently drop buffered spans.
	exporterCtx, exporterCancel := context.WithTimeout(ctx, shutdownGrace)
	exporterErr := exporterShutdown(exporterCtx)
	exporterCancel()

	tpCtx, tpCancel := context.WithTimeout(ctx, shutdownGrace)
	tpErr := tpShutdown(tpCtx)
	tpCancel()

	return errors.Join(exporterErr, tpErr)
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
