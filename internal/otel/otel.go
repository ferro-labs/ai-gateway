package otel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/envref"
	"github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/observability"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
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

// otlpExportTimeout bounds a single OTLP export over http/protobuf.
//
// It matches otlpconfig.DefaultTimeout, the bound the SDK would have applied
// itself. Supplying a custom http.Client is what makes restating it necessary:
// otlptracehttp.NewClient reads cfg.Traces.Timeout only when no client was
// handed in, so both that default and any otlptracehttp.WithTimeout are
// discarded here, and whatever this constant says becomes the only per-export
// bound. It is deliberately not larger than the SDK's: the export is retried
// under a 60s MaxElapsedTime, so widening one attempt buys fewer attempts.
//
// It must also stay POSITIVE. httpclient.New(0) returns the shared default
// client, whose transport is otelhttp-wrapped — the exporter would then trace
// its own exports, each generating further spans to export. Measured: 1
// collector request on the raw transport versus 59 and climbing on the wrapped
// one. A positive timeout takes the raw-transport path.
const otlpExportTimeout = 10 * time.Second

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

	// Before the exporter exists, so a failure building it is already reported
	// at error level rather than through the standard log package.
	installErrorHandler()

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
			logger.Default().Warn("otel: span exporter shutdown after resource build failure",
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
	// (plugin/manager.go) and MCP tool spans (mcp/executor.go) —
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
			logger.Default().Warn("otel: exporter not registered; skipping",
				"name", ec.Name,
			)
			continue
		}
		ex := factory()
		// Resolve ${VAR} references into the exporter's own config here. The Config
		// keeps the references, so an exporter API key is never persisted to the
		// config store nor served by GET /admin/config.
		exCfg, err := envref.AnyMap(ec.Config)
		if err != nil {
			logger.Default().Warn("otel: exporter config has undefined environment references; skipping",
				"name", ec.Name,
				"error", err,
			)
			continue
		}
		if err := ex.Init(ctx, exCfg); err != nil {
			logger.Default().Warn("otel: exporter Init failed; skipping",
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
// The endpoint is resolved by resolveExportTarget, which follows the OTLP
// exporter specification. Transport security comes from the endpoint's scheme —
// https:// is TLS, http:// is plaintext — and a bare host:port keeps its
// historical plaintext meaning.
func newSpanExporter(ctx context.Context, cfg Config) (sdktrace.SpanExporter, error) {
	httpProtocol := isHTTPProtocol(cfg.Protocol)
	target, err := resolveExportTarget(cfg, os.Getenv, httpProtocol)
	if err != nil {
		return nil, err
	}
	target.log()

	h := resolveHeaders(cfg.Headers)

	if httpProtocol {
		var opts []otlptracehttp.Option
		switch {
		case target.url != "":
			opts = append(opts, otlptracehttp.WithEndpointURL(target.url))
		case target.hostPort != "":
			opts = append(opts, otlptracehttp.WithEndpoint(target.hostPort), otlptracehttp.WithInsecure())
		}
		if len(h) > 0 {
			opts = append(opts, otlptracehttp.WithHeaders(h))
		}
		// The SDK builds its own http.Client with no CheckRedirect, so it would
		// follow up to ten hops and replay the exporter headers — which the
		// config documents as holding ${ENV_VAR} secrets — to a host the
		// operator never configured (SEC-007). Hand it the gateway's shared
		// no-redirect client instead.
		//
		// Supplying a client makes otlpExportTimeout the only per-export bound
		// the SDK will honour — see its godoc for why the value is what it is
		// and why it must stay positive.
		opts = append(opts, otlptracehttp.WithHTTPClient(httpclient.New(otlpExportTimeout)))
		return otlptrace.New(ctx, otlptracehttp.NewClient(opts...))
	}

	var opts []otlptracegrpc.Option
	switch {
	case target.url != "":
		opts = append(opts, otlptracegrpc.WithEndpointURL(target.url))
	case target.hostPort != "":
		opts = append(opts, otlptracegrpc.WithEndpoint(target.hostPort), otlptracegrpc.WithInsecure())
	}
	if len(h) > 0 {
		opts = append(opts, otlptracegrpc.WithHeaders(h))
	}
	return otlptrace.New(ctx, otlptracegrpc.NewClient(opts...))
}

// resolveHeaders materialises OTLP export headers from the configuration map.
// Values are substituted through the shared internal/envref resolver — the same one
// plugins, exporters and MCP use, so ${VAR} means exactly one thing everywhere, and
// a literal "$" is always data.
//
// Headers are resolved independently: a header that ends up empty is dropped
// rather than sent blank, since an empty auth header is never intentional and
// would only earn a 401 from the backend. A header with an undefined reference
// is dropped with a warning naming it, rather than failing startup or discarding
// every other header — tracing is auxiliary, and one mistyped trace header must
// not take the rest down with it.
func resolveHeaders(raw map[string]string) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		resolved, err := envref.Expand(v)
		if err != nil {
			logger.Default().Warn("otel: tracing header has an undefined environment reference; dropping it", "header", k, "error", err)
			continue
		}
		if resolved == "" {
			logger.Default().Warn("otel: tracing header resolved to an empty value; dropping it", "header", k)
			continue
		}
		out[k] = resolved
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sampler returns the head sampler for cfg.SampleRatio, wrapped in ParentBased
// so a sampling decision made upstream is honoured rather than retaken.
//
// ParentBased is the specification's default sampler — "The default sampler is
// ParentBased(root=AlwaysOn)"
// (https://opentelemetry.io/docs/specs/otel/trace/sdk/#built-in-samplers) — and
// the three cases below are exactly its parentbased_always_on,
// parentbased_always_off and parentbased_traceidratio configurations.
//
// The reason is trace completeness. A bare ratio sampler ignores the inbound
// SampledFlag and re-decides for every service, so a trace the caller sampled is
// dropped here and the caller is left holding a span whose children do not
// exist. The specification says as much of TraceIdRatioBased: "The
// TraceIdRatioBased MUST ignore the parent SampledFlag. To respect the parent
// SampledFlag, the TraceIdRatioBased should be used as a delegate of the
// ParentBased sampler."
//
// So sample_ratio now governs *root* spans — traces this gateway starts — while
// a request arriving with a traceparent follows the decision already made
// upstream, in both directions: sampled upstream is recorded here even at
// ratio 0, and unsampled upstream is dropped here even at ratio 1.
func sampler(cfg Config) sdktrace.Sampler {
	switch {
	case cfg.SampleRatio >= 1.0:
		return sdktrace.ParentBased(sdktrace.AlwaysSample())
	case cfg.SampleRatio <= 0:
		return sdktrace.ParentBased(sdktrace.NeverSample())
	default:
		return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))
	}
}

// serviceName returns the configured service.name, defaulting to
// "ferrogw" when unset.
func serviceName(cfg Config) string {
	if cfg.ServiceName != "" {
		return cfg.ServiceName
	}
	return "ferrogw"
}

// installErrorHandler routes the OTel SDK's own errors — a failed export above
// all — through the gateway's logger at error level.
//
// Without it they reach the default handler, which prints through the standard
// log package; slog.SetDefault redirects that to the gateway's handler at INFO.
// An export that never leaves the process is not information, and INFO is below
// the level most deployments ship at, so tracing could be entirely broken and
// say so only in a line nobody was going to read.
func installErrorHandler() {
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		logger.Default().Error("otel: sdk error", "error", err)
	}))
}

// noopShutdown is the placeholder Shutdown function returned alongside
// a NoOp Provider.
func noopShutdown(_ context.Context) error { return nil }
