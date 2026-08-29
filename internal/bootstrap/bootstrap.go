package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/internal/admin/handlers"
	"github.com/ferro-labs/ai-gateway/internal/admin/repository"
	"github.com/ferro-labs/ai-gateway/internal/httpserver"
	gwotel "github.com/ferro-labs/ai-gateway/internal/otel"
	"github.com/ferro-labs/ai-gateway/internal/requestlog"
	"github.com/ferro-labs/ai-gateway/internal/version"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
	"github.com/ferro-labs/ai-gateway/pkg/metrics"
	"github.com/ferro-labs/ai-gateway/pkg/ratelimit"
	"github.com/ferro-labs/ai-gateway/providers"
	bedrockpkg "github.com/ferro-labs/ai-gateway/providers/bedrock"
)

// IsProduction reports whether GATEWAY_ENV names the production environment.
// Unset, or any other value, is non-production.
func IsProduction() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("GATEWAY_ENV")), "production")
}

// corsOriginsWildcard reports whether a CORS_ORIGINS value contains a "*"
// entry. Origins are matched literally against the request's Origin header, so
// "*" matches nothing a browser will ever send: an operator who writes it
// asking for allow-all gets allow-nothing, silently.
// Quotes are stripped before the comparison. A value written `CORS_ORIGINS="*"`
// in a compose list entry arrives with its quotes intact, so the check saw the
// three-character string `"*"`, let it through, and the middleware then
// registered a literal origin no browser can send — the exact allow-nothing
// state the refusal exists to prevent, reached by the spelling an operator is
// most likely to use.
func corsOriginsWildcard(value string) bool {
	for _, origin := range strings.Split(value, ",") {
		if strings.Trim(strings.TrimSpace(origin), `"'`) == "*" {
			return true
		}
	}
	return false
}

// CheckProductionSafety returns an error naming every production setting the
// gateway refuses to serve under. It reports all of them at once, so fixing a
// production deployment is one edit rather than a restart per problem. Outside
// production (GATEWAY_ENV unset or any other value) it always returns nil, and
// the same settings are honoured with a startup warning.
//
// Refusing to start is reserved for a setting that cannot become correct on its
// own and cannot do what the operator asked:
//
//   - ALLOW_UNAUTHENTICATED_PROXY=true removes all /v1/* data-plane auth.
//   - CORS_ORIGINS containing "*" is not a wildcard here; it is one literal
//     origin no browser sends, so it denies every cross-origin request while
//     reading like it permits them all. Every listed origin has to be spelled
//     out.
//
// Settings that are merely risky are warnings instead — see
// warnProductionRisks. A gateway that refuses to boot over a defensible
// deployment choice is a crash loop, and the crash loop is the bigger outage.
func CheckProductionSafety() error {
	if !IsProduction() {
		return nil
	}

	var problems []string
	if strings.EqualFold(strings.TrimSpace(os.Getenv("ALLOW_UNAUTHENTICATED_PROXY")), "true") {
		problems = append(problems,
			"ALLOW_UNAUTHENTICATED_PROXY=true would leave all /v1/* data-plane endpoints unauthenticated; "+
				"unset it or change GATEWAY_ENV to a non-production value")
	}
	if corsOriginsWildcard(os.Getenv("CORS_ORIGINS")) {
		problems = append(problems,
			`CORS_ORIGINS contains "*", which is matched literally rather than as a wildcard and therefore `+
				"allows no cross-origin request at all; list each allowed origin explicitly, or unset CORS_ORIGINS "+
				"to serve no CORS headers")
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("GATEWAY_ENV=production rejects this configuration: %s", strings.Join(problems, "; "))
}

// warnProductionRisks reports, once at startup, every production setting that
// is allowed but worth knowing is in force. Each is a defensible deployment
// choice, which is exactly why none of them blocks startup:
//
//   - Per-IP rate limiting off (RATE_LIMIT_RPS=0) is normal when limits are
//     enforced by an ingress or API gateway in front of this one.
//   - Profiling routes mounted (ENABLE_PPROF) is how a production incident gets
//     diagnosed. They sit behind the admin scope; the risk is leaving them
//     mounted past the incident, since a profile is a memory image.
//   - An in-memory API key store loses every operator key, dashboard session and
//     audit entry on restart, leaving MASTER_KEY as the only way back in. It is
//     the documented default and serves correctly until the process restarts.
//
// Silence is the failure mode being fixed: each of these was previously either
// an INFO line or nothing at all, so a production gateway could not be told
// apart from a laptop by reading its own startup log.
// rateLimitDisabled is what the limiter actually resolved to, not what the
// environment says. Re-reading RATE_LIMIT_RPS here and comparing it as a string
// answered a different question than the parse the limiter performs, and drifted
// in both directions: "0.0" and "00" disabled limiting with no warning at all,
// while " 0 " warned about a limiter that was still running at the default rate.
func warnProductionRisks(keyStoreBackend string, rateLimitDisabled bool) {
	if !IsProduction() {
		return
	}
	if rateLimitDisabled {
		logger.Default().Warn("production: per-IP rate limiting is disabled by RATE_LIMIT_RPS -- the gateway applies no request-rate limit of its own")
	}
	if httpserver.PprofEnabled() {
		logger.Default().Warn("production: ENABLE_PPROF mounts /debug/pprof -- profiles are memory images that can contain request bodies and credentials; they require the admin scope, and are best unmounted once an investigation ends")
	}
	if keyStoreBackend == BackendMemory {
		logger.Default().Warn("production: the API key store is in-memory -- operator keys, dashboard sessions and the audit trail are lost on restart, leaving MASTER_KEY as the only way back in; set API_KEY_STORE_BACKEND=sqlite or postgres")
	}
}

// defaultListenAddr is the address the HTTP server listens on when PORT is unset.
const defaultListenAddr = ":8080"

// shutdownGracePeriod bounds the initial drain attempt before shutdown reports
// a timeout and continues waiting with dependencies still available.
const shutdownGracePeriod = 15 * time.Second

// Serve runs the full gateway server startup sequence and blocks until the
// server shuts down or ctx is canceled.
func Serve(ctx context.Context) error {
	// Configure the process logger from the environment (LOG_LEVEL) and install
	// it as the default (mirrored into slog.SetDefault). lg is then threaded
	// explicitly into the components that hold their own logger.
	lg := logger.New(logger.FromEnv())
	logger.SetDefault(lg)
	if ctx.Err() != nil {
		return nil
	}

	if err := CheckProductionSafety(); err != nil {
		lg.Error("startup blocked: unsafe configuration", "error", err)
		return err
	}

	app, err := buildServer(ctx, lg)
	if err != nil {
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			return nil
		}
		return err
	}
	listenErr := runUntilShutdown(ctx, app.gw, app.srv)
	shutdownErr := gracefulShutdown(app, shutdownGracePeriod)
	if errors.Is(listenErr, http.ErrServerClosed) || (ctx.Err() != nil && errors.Is(listenErr, ctx.Err())) {
		listenErr = nil
	}
	return errors.Join(listenErr, shutdownErr)
}

type serverRuntime struct {
	gw           *aigateway.Gateway
	srv          *http.Server
	cfgManager   handlers.ConfigManager
	keyStore     repository.Store
	sessionStore repository.SessionStore
	auditStore   repository.AuditStore
	logReader    requestlog.Reader
	otelShutdown gwotel.ShutdownFunc
}

func (app *serverRuntime) resources() []httpserver.NamedResource {
	return []httpserver.NamedResource{
		{Name: "gateway", Value: app.gw},
		{Name: "config manager", Value: app.cfgManager},
		{Name: "api key store", Value: app.keyStore},
		{Name: "session store", Value: app.sessionStore},
		{Name: "audit store", Value: app.auditStore},
		{Name: "request log store", Value: app.logReader},
	}
}

func closeRuntimeResources(resources []httpserver.NamedResource, otelShutdown gwotel.ShutdownFunc) error {
	err := httpserver.CloseResources(resources...)
	if otelShutdown != nil {
		err = errors.Join(err, otelShutdown(context.Background()))
	}
	return err
}

// buildServer runs the startup sequence: it loads configuration, registers
// providers, initializes observability and the persistence stores, constructs
// the router and HTTP server, prints the startup banner, and logs readiness.
// It returns startup errors and the long-lived components needed to run and
// later shut down the server.
func buildServer(ctx context.Context, lg *logger.Logger) (app *serverRuntime, err error) {
	app = &serverRuntime{}
	started := app
	defer func() {
		if err == nil {
			return
		}
		if closeErr := closeRuntimeResources(started.resources(), started.otelShutdown); closeErr != nil {
			// Logged here because the CLI silences the returned error: startup
			// already reported its own failure, and this one must not be lost with it.
			logger.Default().Error("startup cleanup error", "error", closeErr)
			err = errors.Join(err, closeErr)
		}
	}()

	cfg, err := LoadConfig()
	if err != nil {
		return nil, err
	}
	// Ahead of RegisterProviders, so an operator reads the rename before the
	// line reporting the provider that was built from the deprecated variable.
	LogDeprecatedEnvVars()
	registry := RegisterProviders()
	masterKey := ResolveMasterKey()

	if strings.EqualFold(strings.TrimSpace(os.Getenv("ALLOW_UNAUTHENTICATED_PROXY")), "true") {
		logger.Default().Warn("ALLOW_UNAUTHENTICATED_PROXY is set -- proxy routes are unauthenticated (dev mode only; not for production)")
	}

	if len(registry.List()) == 0 {
		logger.Default().Warn("no providers configured; set provider API keys (e.g. OPENAI_API_KEY) or OLLAMA_HOST before starting -- providers are registered at startup and cannot be added to a running process")
	}

	// Build the request-log store before the gateway so it can be handed to
	// logging plugins as they load. The reader is also the writer (one
	// *SQLWriter serves both the admin log views and the plugin), or nil when
	// no request-log backend is configured.
	logReader, logMaintainer, logReaderBackend, err := CreateRequestLogReaderFromEnv(ctx)
	if err != nil {
		logger.Default().Error("failed to initialize request log reader", "error", err)
		return nil, err
	}
	app.logReader = logReader
	logWriter, _ := logReader.(requestlog.Writer)

	// Initialise OpenTelemetry. Init returns a NoOp provider (and a
	// no-op shutdown) when neither an OTLP endpoint nor any enabled
	// exporter is configured, so this is free for users who don't opt in.
	//
	// This runs BEFORE the gateway is built, and the order is load-bearing.
	// Constructing the gateway starts MCP initialization in the background, and
	// those handshakes open spans through the global tracer. Installing the
	// TracerProvider afterwards left every span opened in that window
	// non-recording, so mcp.init_server and mcp.initialize were never exported
	// and mcp.list_tools arrived at the collector as a parentless root.
	var obsCfg config.ObservabilityConfig
	if cfg != nil {
		obsCfg = cfg.Observability
	}
	obsProvider, otelShutdown, err := gwotel.Init(ctx, otelConfigFromGateway(obsCfg))
	if err != nil {
		logger.Default().Error("failed to initialize observability", "error", err)
		return nil, err
	}
	app.otelShutdown = otelShutdown

	gw, err := BuildGateway(ctx, cfg, registry, logWriter, lg)
	if err != nil {
		return nil, err
	}
	app.gw = gw
	gw.SetObservability(obsProvider)

	// Resolve gateway_circuit_breaker_state from this gateway's live breakers on
	// every scrape, rather than from whenever a request last finished.
	gw.PublishCircuitBreakerMetrics()

	cfgManager, configStoreBackend, err := CreateConfigManagerFromEnv(ctx, gw)
	if err != nil {
		logger.Default().Error("failed to initialize config store", "error", err)
		return nil, err
	}
	app.cfgManager = cfgManager

	// Config resolution is complete only here: the store, when it holds one,
	// has just superseded whatever the gateway was built from. Everything
	// logged above this point describes a source, not the outcome — so the
	// outcome is named once, now, and the banner below is drawn from it rather
	// than from the file.
	active := ResolveActiveConfig(gw, cfgManager, cfg, configStoreBackend)

	keyStore, keyStoreBackend, err := CreateKeyStoreFromEnv(ctx)
	if err != nil {
		logger.Default().Error("failed to initialize API key store", "error", err)
		return nil, err
	}
	app.keyStore = keyStore

	// The session store follows the key store's backend so an operator
	// configures persistence once (see CreateSessionStoreFromEnv). It is a
	// distinct store, and for SQLite a distinct database file, from the key
	// store built above.
	sessionStore, _, err := CreateSessionStoreFromEnv(ctx)
	if err != nil {
		logger.Default().Error("failed to initialize session store", "error", err)
		return nil, err
	}
	app.sessionStore = sessionStore

	// The audit store follows the key store's backend too, for the same reason
	// (see CreateAuditStoreFromEnv). It is a distinct store and, for SQLite, a
	// distinct file from the key, session, and config stores.
	auditStore, auditStoreBackend, err := CreateAuditStoreFromEnv(ctx)
	if err != nil {
		logger.Default().Error("failed to initialize audit store", "error", err)
		return nil, err
	}
	app.auditStore = auditStore

	var corsOrigins []string
	if origins := os.Getenv("CORS_ORIGINS"); origins != "" {
		corsOrigins = strings.Split(origins, ",")
		// Production refuses this outright (CheckProductionSafety), so this
		// line is what a non-production run gets instead of silence.
		if corsOriginsWildcard(origins) {
			logger.Default().Warn(`CORS_ORIGINS contains "*", which is matched literally, not as a wildcard -- no cross-origin request will be allowed; list each origin explicitly`)
		}
	}

	// TRUSTED_PROXIES is a comma-separated list of CIDR blocks whose
	// X-Forwarded-For / X-Real-IP headers are trusted for client-IP resolution.
	// An empty or unset value falls back to the loopback default
	// (127.0.0.0/8, ::1/128), which is appropriate for a local reverse proxy.
	trustedProxies, err := httpserver.ParseTrustedProxyCIDRs(os.Getenv("TRUSTED_PROXIES"))
	if err != nil {
		logger.Default().Error("invalid TRUSTED_PROXIES", "error", err)
		return nil, err
	}

	rlStore := NewRateLimitStore()

	// After the stores and the rate limiter exist, so the report describes what
	// this process actually ended up with rather than what was asked for.
	warnProductionRisks(keyStoreBackend, rlStore == nil)

	r := httpserver.NewRouter(registry, keyStore, sessionStore, corsOrigins, gw, cfgManager, rlStore, logReader, logMaintainer, auditStore, masterKey, trustedProxies)

	addr := defaultListenAddr
	if p := os.Getenv("PORT"); p != "" {
		addr = ":" + p
	}
	srv := httpserver.NewServer(addr, r)
	app.srv = srv

	PrintStartupBanner(addr, registry, &active, masterKey, keyStoreBackend, configStoreBackend)
	logger.Default().Info("ferrogw started",
		"version", version.Short(),
		"addr", addr,
		"providers", len(registry.List()),
		"config_store", configStoreBackend,
		"api_key_store", keyStoreBackend,
		"request_log_store", logReaderBackend,
		"audit_store", auditStoreBackend,
	)

	return app, nil
}

// runUntilShutdown starts the HTTP server and optional live model discovery,
// then blocks until an OS signal or a fatal listen error. It returns the listen
// error observed, if any.
func runUntilShutdown(ctx context.Context, gw *aigateway.Gateway, srv *http.Server) error {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", srv.Addr)
	if err != nil {
		logger.Default().Error("server error", "error", err)
		return err
	}

	// Bind before observing cancellation so graceful shutdown cannot race a
	// not-yet-started ListenAndServe goroutine and leave a listener behind.
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(listener) }()

	// Block until OS signal or a fatal server error.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)

	// Opt-in live model discovery: refreshes provider model lists in the
	// background and stops when the lifecycle ctx is cancelled on shutdown.
	if interval, ok := discoveryIntervalFromEnv(); ok {
		if err := gw.StartDiscovery(ctx, interval); err != nil {
			logger.Default().Warn("model discovery not started", "error", err)
		} else {
			logger.Default().Info("model discovery enabled", "interval", interval.String())
		}
	}

	var listenErr error
	select {
	case <-ctx.Done():
		logger.Default().Info("shutdown signal received")
	case listenErr = <-serveErr:
		if listenErr != nil && !errors.Is(listenErr, http.ErrServerClosed) {
			logger.Default().Error("server error", "error", listenErr)
		}
	}
	stop()
	return listenErr
}

// gracefulShutdown drains the HTTP server, closes the persistence stores, and
// flushes OTel exporters in that order — the same list, in the same order, that
// a failed startup closes (see buildServer).
func gracefulShutdown(app *serverRuntime, gracePeriod time.Duration) error {
	// Shutdown drains active connections before returning — the stores must
	// close after so in-flight requests can still reach them.
	logger.Default().Info("shutting down gracefully")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), gracePeriod)
	shutdownErr := app.srv.Shutdown(shutdownCtx)
	cancel()
	if shutdownErr != nil {
		logger.Default().Error("shutdown error", "error", shutdownErr)
		// Shutdown's deadline does not terminate active handlers. Keep shared
		// resources alive and wait for them before cleanup.
		if err := app.srv.Shutdown(context.Background()); err != nil {
			shutdownErr = errors.Join(shutdownErr, err)
		}
	}

	// OTel drains last so spans emitted during the rest of the shutdown
	// sequence still reach the collector. The ShutdownFunc from gwotel.Init
	// applies its own deadline (cfg.ShutdownGrace), hence context.Background().
	cleanupErr := closeRuntimeResources(app.resources(), app.otelShutdown)
	if cleanupErr != nil {
		logger.Default().Error("shutdown cleanup error", "error", cleanupErr)
	}

	logger.Default().Info("server stopped")
	return errors.Join(shutdownErr, cleanupErr)
}

// discoveryIntervalFromEnv reads the FERRO_MODEL_DISCOVERY_INTERVAL env var and
// returns the opt-in refresh interval for live model discovery. It is pure: it
// performs no logging. Returns (0, false) when the var is unset/empty, fails to
// parse, or resolves to a duration below the 1-minute minimum (which guards
// against a hot-loop of provider API calls); otherwise (interval, true).
func discoveryIntervalFromEnv() (time.Duration, bool) {
	raw := strings.TrimSpace(os.Getenv("FERRO_MODEL_DISCOVERY_INTERVAL"))
	if raw == "" {
		return 0, false
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < time.Minute {
		return 0, false
	}
	return d, true
}

// ResolveMasterKey returns the master key from the MASTER_KEY env var.
func ResolveMasterKey() string {
	return strings.TrimSpace(os.Getenv("MASTER_KEY"))
}

// LogDeprecatedEnvVars warns about environment variables the gateway still
// honours but is going to stop reading.
func LogDeprecatedEnvVars() {
	if strings.TrimSpace(os.Getenv("OLLAMA_MODELS")) != "" {
		logger.Default().Warn("OLLAMA_MODELS is deprecated -- use FERRO_OLLAMA_MODELS instead; " +
			"OLLAMA_MODELS is Ollama's own variable for its models directory and will stop being read")
	}
}

// configFilePath returns the config path the operator named, or "" when
// GATEWAY_CONFIG is unset.
func configFilePath() string {
	return strings.TrimSpace(os.Getenv("GATEWAY_CONFIG"))
}

// wellKnownConfigPaths are the container locations a config file is
// conventionally mounted at. The gateway does not read them — see
// warnUnreadConfigFile.
var wellKnownConfigPaths = []string{
	"/etc/ferrogw/config.yaml",
	"/etc/ferrogw/config.yml",
	"/etc/ferrogw/config.json",
}

// warnUnreadConfigFile reports a config file sitting at a conventional path
// that the gateway is not reading because GATEWAY_CONFIG is unset.
//
// It warns and does not load. Reading an unnamed file would mean a stray file
// on a host silently changes how a gateway routes — the opposite failure, and a
// worse one. The explicit-source rule is what kubelet follows: it takes
// --config and discovers nothing. But a mounted file the process ignores in
// silence is how an operator ends up believing a narrowed target list is in
// force when every credentialled provider is still routable, so the file is
// worth naming.
func warnUnreadConfigFile() {
	for _, path := range wellKnownConfigPaths {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			logger.Default().Warn(
				"a config file exists at a well-known path but GATEWAY_CONFIG is unset, so it is ignored; "+
					"set GATEWAY_CONFIG to this path to apply it",
				"path", path,
			)
		}
	}
}

// LoadConfig loads and validates the gateway config from GATEWAY_CONFIG env var.
// Returns nil if GATEWAY_CONFIG is not set (caller uses default config).
//
// What it returns is the *file's* config, which is not necessarily the config
// the gateway ends up running: a persistent config store, if it holds one,
// supersedes this later in startup. ResolveActiveConfig names the winner.
func LoadConfig() (*config.Config, error) {
	cfgPath := configFilePath()
	if cfgPath == "" {
		warnUnreadConfigFile()
		return nil, nil
	}
	loaded, err := config.LoadConfig(cfgPath)
	if err != nil {
		logger.Default().Error("failed to load config", "error", err)
		return nil, err
	}
	if err := config.ValidateConfig(*loaded); err != nil {
		logger.Default().Error("invalid config", "error", err)
		return nil, err
	}
	// "config file loaded" rather than "config loaded": this line describes the
	// file, and a persisted config may still replace all of it below.
	logger.Default().Info("config file loaded",
		"path", cfgPath,
		"strategy", loaded.Strategy.Mode,
		"targets", len(loaded.Targets),
	)
	return loaded, nil
}

// RegisterProviders auto-registers all providers found via environment variables.
func RegisterProviders() *providers.Registry {
	registry := providers.NewRegistry()
	registerProviderEntries(registry, providers.AllProviders())
	registerBedrockProvider(registry)
	return registry
}

func registerProviderEntries(registry *providers.Registry, entries []providers.ProviderEntry) {
	for _, entry := range entries {
		if entry.ID == providers.NameBedrock {
			continue // handled below with its dual-key detection
		}

		cfg := providers.ProviderConfigFromEnv(entry)
		if cfg == nil {
			continue // required env var unset — provider not configured, skip silently
		}

		p, err := entry.Build(cfg)
		if err != nil {
			// Warn-and-skip so one bad credential cannot stop the whole gateway.
			// The counter is what keeps that from being a silent partial outage:
			// /health only counts registered providers, so it stays 200 here.
			logger.Default().Warn("provider init skipped", "provider", entry.ID, "error", err)
			metrics.ProviderInitFailures.WithLabelValues(entry.ID).Inc()
			continue
		}
		registry.Register(p)
		logger.Default().Info("provider registered", "provider", entry.ID)
	}
}

func registerBedrockProvider(registry *providers.Registry) {
	// AWS Bedrock: register if AWS_REGION, AWS_ACCESS_KEY_ID, or
	// AWS_BEARER_TOKEN_BEDROCK is set.
	if region := os.Getenv("AWS_REGION"); region != "" ||
		os.Getenv("AWS_ACCESS_KEY_ID") != "" ||
		os.Getenv("AWS_BEARER_TOKEN_BEDROCK") != "" {
		p, err := bedrockpkg.NewWithOptions(bedrockpkg.Options{
			Region:          os.Getenv("AWS_REGION"),
			BearerToken:     os.Getenv("AWS_BEARER_TOKEN_BEDROCK"),
			AccessKeyID:     os.Getenv("AWS_ACCESS_KEY_ID"),
			SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
			SessionToken:    os.Getenv("AWS_SESSION_TOKEN"),
		})
		if err != nil {
			// Same warn-and-skip contract as registerProviderEntries: the counter
			// is the only machine-readable signal that this provider is missing.
			logger.Default().Error("provider init failed", "provider", providers.NameBedrock, "error", err)
			metrics.ProviderInitFailures.WithLabelValues(providers.NameBedrock).Inc()
		} else {
			registry.Register(p)
			logger.Default().Info("provider registered", "provider", providers.NameBedrock, "region", p.Region())
		}
	}
}

// BuildGateway constructs the Gateway, wires providers, and loads plugins.
// If cfg is nil a default fallback config is created from the registry.
func BuildGateway(ctx context.Context, cfg *config.Config, registry *providers.Registry, logWriter requestlog.Writer, lg *logger.Logger) (*aigateway.Gateway, error) {
	if cfg == nil {
		defaultTargets := make([]config.Target, 0, len(registry.List()))
		for _, name := range registry.List() {
			defaultTargets = append(defaultTargets, config.Target{VirtualKey: name})
		}
		cfg = &config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeFallback},
			Targets:  defaultTargets,
		}
		logger.Default().Info("using default config",
			"strategy", cfg.Strategy.Mode,
			"targets", len(cfg.Targets),
		)
	}

	gw, err := aigateway.New(*cfg, aigateway.WithLogger(lg), aigateway.WithContext(ctx))
	if err != nil {
		logger.Default().Error("failed to create gateway", "error", err)
		return nil, err
	}
	// Install the shared request-log store before LoadPlugins, so a logging
	// plugin records through it instead of opening its own.
	gw.SetRequestLogWriter(logWriter)
	for _, name := range registry.List() {
		if p, ok := registry.Get(name); ok {
			gw.RegisterProvider(p)
		}
	}
	warnUnroutableTargets(gw)
	if len(cfg.Plugins) > 0 {
		if err := gw.LoadPlugins(); err != nil {
			logger.Default().Error("failed to load plugins", "error", err)
			_ = gw.Close()
			return nil, err
		}
		logger.Default().Info("plugins loaded", "count", len(cfg.Plugins))
	}
	return gw, nil
}

// warnUnroutableTargets reports every configured target that resolves to no
// registered provider, once, at startup.
//
// It warns rather than exits. A provider is registered only when its credential
// is present, so a target naming a provider whose key has not been rolled out
// yet is a legitimate config that starts serving the moment the secret lands —
// refusing to boot would turn a staged credential rollout into a crash loop.
// The same reasoning is why an xDS-driven proxy accepts a route naming a cluster
// it does not have yet while a fully static one refuses to load: a reference
// that can still become valid is not the same fault as one that cannot.
//
// Warning is not the whole answer, only the part that belongs at startup. An
// unroutable target also shows up in Gateway.Readiness, so /readyz reports it,
// and takes the instance out of rotation when *no* target resolves. That is the
// signal an orchestrator watches; this line is what an operator greps for.
func warnUnroutableTargets(gw *aigateway.Gateway) {
	readiness := gw.Readiness()
	unroutable := make([]string, 0, len(readiness.Targets))
	for _, t := range readiness.Targets {
		if !t.Routable {
			unroutable = append(unroutable, t.Name)
		}
	}
	if len(unroutable) == 0 {
		return
	}
	if len(unroutable) == len(readiness.Targets) {
		logger.Default().Error(
			"no configured target resolves to a registered provider; every request will fail with a routing error",
			"targets", strings.Join(unroutable, ","))
		return
	}
	logger.Default().Warn(
		"configured target has no registered provider; requests routed to it will fail",
		"targets", strings.Join(unroutable, ","))
}

const (
	// defaultRateLimitRPS and defaultRateLimitBurst are applied when
	// RATE_LIMIT_RPS/RATE_LIMIT_BURST are unset, so the per-IP limiter is
	// enabled out of the box rather than opt-in.
	defaultRateLimitRPS   = 20.0
	defaultRateLimitBurst = 40.0
)

// defaultRateLimitMaxKeys caps the number of per-IP limiters tracked at
// once, preventing unbounded memory growth from a flood of distinct client
// IPs. A var (not const) so tests can shrink it to exercise the eviction cap
// cheaply; production always uses this value.
var defaultRateLimitMaxKeys = 100_000

// NewRateLimitStore builds a per-IP token-bucket store from env vars. Rate
// limiting is enabled by default at defaultRateLimitRPS/defaultRateLimitBurst,
// capped at defaultRateLimitMaxKeys tracked IPs. Set RATE_LIMIT_RPS=0 to opt
// out entirely; a positive RATE_LIMIT_RPS overrides the default rate. Setting
// RATE_LIMIT_RPS alone always resets the burst to defaultRateLimitBurst too —
// set RATE_LIMIT_BURST explicitly if a custom rate needs a matching burst.
//
// The store keys on the resolved client IP (see RealIPMiddleware), which only
// trusts X-Forwarded-For/X-Real-IP from TRUSTED_PROXIES (default: loopback).
// Behind a reverse proxy/ingress/LB outside that range, every request
// resolves to the proxy's own IP, collapsing this into one shared bucket for
// all clients — set TRUSTED_PROXIES to the proxy's real CIDR for this to
// rate-limit per client rather than globally.
func NewRateLimitStore() *ratelimit.Store {
	rps := defaultRateLimitRPS
	if rpsStr := os.Getenv("RATE_LIMIT_RPS"); rpsStr != "" {
		v, err := strconv.ParseFloat(rpsStr, 64)
		switch {
		case err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0:
			logger.Default().Warn("ignoring invalid RATE_LIMIT_RPS; using default", "value", rpsStr, "default", defaultRateLimitRPS)
		case v == 0:
			logger.Default().Info("rate limiting disabled via RATE_LIMIT_RPS=0")
			return nil
		default:
			rps = v
		}
	}

	burst := defaultRateLimitBurst
	if burstStr := os.Getenv("RATE_LIMIT_BURST"); burstStr != "" {
		if v, err := strconv.ParseFloat(burstStr, 64); err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 {
			burst = v
		} else {
			logger.Default().Warn("ignoring invalid RATE_LIMIT_BURST; using default", "value", burstStr, "default", defaultRateLimitBurst)
		}
	}

	store := ratelimit.NewStoreWithMax(rps, burst, defaultRateLimitMaxKeys)
	logger.Default().Info("rate limiting enabled", "rps", rps, "burst", burst, "max_keys", defaultRateLimitMaxKeys)
	return store
}

// PrintStartupBanner prints a branded, informative banner to stderr on server start.
func PrintStartupBanner(addr string, registry *providers.Registry, cfg *config.Config, masterKey, keyStoreBackend, configStoreBackend string) {
	const (
		orange = "\033[38;5;208m"
		bold   = "\033[1m"
		white  = "\033[97m"
		dim    = "\033[2m"
		green  = "\033[92m"
		yellow = "\033[93m"
		reset  = "\033[0m"
	)

	strategy := "fallback"
	pluginCount := 0
	if cfg != nil {
		strategy = string(cfg.Strategy.Mode)
		pluginCount = len(cfg.Plugins)
	}

	providerCount := len(registry.List())

	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "  %sFERRO LABS  AI GATEWAY%s  %s%s%s\n",
		bold+white, reset, dim, version.Short(), reset)
	fmt.Fprintf(os.Stderr, "  %s->%s  http://localhost%s\n",
		orange, reset, addr)
	fmt.Fprintf(os.Stderr, "\n")

	// Provider status.
	fmt.Fprintf(os.Stderr, "  Providers\n")
	topProviders := []struct {
		id     string
		envVar string
	}{
		{"openai", "OPENAI_API_KEY"},
		{"anthropic", "ANTHROPIC_API_KEY"},
		{"gemini", "GEMINI_API_KEY"},
		{"groq", "GROQ_API_KEY"},
		{"mistral", "MISTRAL_API_KEY"},
	}
	for _, tp := range topProviders {
		if _, ok := registry.Get(tp.id); ok {
			fmt.Fprintf(os.Stderr, "    %s[OK]%s %s\n", green, reset, tp.id)
		} else {
			fmt.Fprintf(os.Stderr, "    %s[-]%s  %s (%s not set)\n", dim, reset, tp.id, tp.envVar)
		}
	}
	topSet := map[string]bool{"openai": true, "anthropic": true, "gemini": true, "groq": true, "mistral": true}
	for _, name := range registry.List() {
		if !topSet[name] {
			fmt.Fprintf(os.Stderr, "    %s[OK]%s %s\n", green, reset, name)
		}
	}
	fmt.Fprintf(os.Stderr, "    %s%d providers | %s | %d plugins%s\n",
		dim, providerCount, strategy, pluginCount, reset)
	fmt.Fprintf(os.Stderr, "\n")

	// Auth status.
	fmt.Fprintf(os.Stderr, "  Auth\n")
	if masterKey != "" {
		fmt.Fprintf(os.Stderr, "    Master key: %sconfigured%s\n", green, reset)
	} else {
		fmt.Fprintf(os.Stderr, "    %s[!] No MASTER_KEY set -- run 'ferrogw init' to generate one%s\n", yellow, reset)
	}
	fmt.Fprintf(os.Stderr, "\n")

	// Store warnings.
	hasWarnings := false
	if keyStoreBackend == BackendMemory {
		if !hasWarnings {
			fmt.Fprintf(os.Stderr, "  Warnings\n")
			hasWarnings = true
		}
		fmt.Fprintf(os.Stderr, "    %s[!] API key store: in-memory (keys lost on restart)%s\n", yellow, reset)
	}
	if configStoreBackend == BackendMemory {
		if !hasWarnings {
			fmt.Fprintf(os.Stderr, "  Warnings\n")
			hasWarnings = true
		}
		fmt.Fprintf(os.Stderr, "    %s[!] Config store: in-memory (config lost on restart)%s\n", yellow, reset)
	}
	if hasWarnings {
		fmt.Fprintf(os.Stderr, "    %sSet API_KEY_STORE_BACKEND=sqlite for persistence%s\n", dim, reset)
		fmt.Fprintf(os.Stderr, "\n")
	}
}
