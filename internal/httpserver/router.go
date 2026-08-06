package httpserver

import (
	"expvar"
	"net"
	"net/http"
	"slices"
	"strings"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/internal/admin/handlers"
	"github.com/ferro-labs/ai-gateway/internal/admin/model"
	"github.com/ferro-labs/ai-gateway/internal/admin/repository"
	"github.com/ferro-labs/ai-gateway/internal/apierror"
	"github.com/ferro-labs/ai-gateway/internal/handler"
	"github.com/ferro-labs/ai-gateway/internal/middleware"
	gwotel "github.com/ferro-labs/ai-gateway/internal/otel"
	"github.com/ferro-labs/ai-gateway/internal/proxy"
	"github.com/ferro-labs/ai-gateway/internal/redact"
	"github.com/ferro-labs/ai-gateway/internal/requestlog"
	"github.com/ferro-labs/ai-gateway/internal/webui"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
	"github.com/ferro-labs/ai-gateway/pkg/ratelimit"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// onlyMethod serves h for the named method and answers 405 for every other
// one, so a route the gateway handles itself refuses a wrong method instead of
// leaving it to match a broader pattern registered later.
//
// A GET route also serves HEAD. RFC 9110 §9.3.2 defines HEAD as GET with the
// response body omitted, so a resource that supports one supports the other,
// and HEAD is what load-balancer probes and cache revalidation send. h needs no
// HEAD branch of its own: net/http discards the body of a HEAD response
// (chunkWriter.write eats the writes), leaving exactly the headers the
// equivalent GET would have produced.
//
// The Allow header is part of the contract: RFC 9110 §15.5.6 requires a 405 to
// name the methods the resource supports, and OpenAI SDKs read it rather than
// retrying. It names every method accepted here, HEAD included.
func onlyMethod(method string, h http.HandlerFunc) http.Handler {
	accepted := []string{method}
	if method == http.MethodGet {
		accepted = append(accepted, http.MethodHead)
	}
	allow := strings.Join(accepted, ", ")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !slices.Contains(accepted, r.Method) {
			w.Header().Set("Allow", allow)
			apierror.WriteOpenAI(w, http.StatusMethodNotAllowed,
				"Only "+allow+" requests are accepted on "+r.URL.Path+".",
				"invalid_request_error", "method_not_supported")
			return
		}
		h(w, r)
	})
}

// routableMethods is the set of request methods chi is able to route. It
// mirrors chi's own method table, which chi does not export.
var routableMethods = map[string]struct{}{
	http.MethodConnect: {},
	http.MethodDelete:  {},
	http.MethodGet:     {},
	http.MethodHead:    {},
	http.MethodOptions: {},
	http.MethodPatch:   {},
	http.MethodPost:    {},
	http.MethodPut:     {},
	http.MethodTrace:   {},
}

// rejectUnknownMethod answers a request whose method no route could ever match,
// before the router is consulted.
//
// chi refuses a method absent from its own table ahead of any route lookup and
// answers 405 with an empty body and no Allow header. That 405 cannot be
// repaired where it is raised: RFC 9110 §15.5.6 requires a 405 to name the
// target resource's supported methods, and a router that never matched a route
// has no resource whose methods to name. Reading them off the routing tree
// instead would be worse than silence — every natively handled path is
// registered for all methods so a wrong method cannot fall through to the
// /v1/* pass-through (see mountOpenAIRoutes), so the tree answers "all of them"
// for a route that serves one.
//
// RFC 9110 §15.6.2 covers this case directly: 501 is "the appropriate response
// when the server does not recognize the request method and is not capable of
// supporting it for any resource". Methods chi can route pass through
// untouched, so a recognised method on the wrong route keeps the accurate 405
// and Allow that the route itself produces.
func rejectUnknownMethod(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := routableMethods[r.Method]; !ok {
			apierror.WriteOpenAI(w, http.StatusNotImplemented,
				"The request method is not supported by this server.",
				"invalid_request_error", "method_not_supported")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// activeMaxRequestBytes returns the body-size cap resolved from the gateway's
// live config on every call, so an operator who changes max_request_bytes
// through the admin API gets the value they were told they got.
//
// It is a function rather than a value on purpose. The cap used to be read once
// while the router was built and captured in the middleware closure, which made
// the admin API's answer and the enforced limit two different numbers: a
// lowered cap returned 200, was reported by GET /admin/config, and let an
// oversized body through until the process restarted. The gateway is the single
// authority for the active config; anything mutable reads from it per request
// instead of keeping a boot-time copy.
//
// The cost is one RLock and one struct copy per request: ~31 ns and zero
// allocations measured, against a provider call three orders of magnitude
// larger. It deliberately does not add a second published snapshot beside
// routingSnapshot: that pattern exists because routing reads several facts that
// only mean anything as a set, and one scalar has no such consistency problem.
func activeMaxRequestBytes(gw *aigateway.Gateway) func() int64 {
	return func() int64 {
		if gw != nil {
			if n := gw.GetConfig().MaxRequestBytes; n > 0 {
				return n
			}
		}
		return config.DefaultMaxRequestBytes
	}
}

// NewRouter builds the HTTP router for the gateway.
//
// trustedProxies lists the CIDR ranges whose X-Forwarded-For / X-Real-IP
// headers are honored for client-IP resolution. Pass nil or an empty slice to
// use only the loopback default (127.0.0.0/8, ::1/128).
func NewRouter(
	registry *providers.Registry,
	keyStore repository.Store,
	sessionStore repository.SessionStore,
	corsOrigins []string,
	gw *aigateway.Gateway,
	cfgManager handlers.ConfigManager,
	rlStore *ratelimit.Store,
	logReader requestlog.Reader,
	logMaintainer requestlog.Maintainer,
	auditStore repository.AuditStore,
	masterKey string,
	trustedProxies []*net.IPNet,
) http.Handler {
	gw = ensureGateway(gw, registry)

	r := chi.NewRouter()

	// Resolve the trusted-proxy CIDR list. When the caller passes nil (e.g.
	// tests), default to the loopback-only set so local reverse proxies are
	// trusted but arbitrary callers cannot forge their source IP.
	resolvedProxies := trustedProxies
	if len(resolvedProxies) == 0 {
		var err error
		resolvedProxies, err = ParseTrustedProxyCIDRs("")
		if err != nil {
			// ParseTrustedProxyCIDRs("") uses hard-coded defaults and never
			// returns an error; panic here would indicate a programmer bug.
			panic("realip: failed to parse default trusted proxy CIDRs: " + err.Error())
		}
	}

	// Root middleware stack: safe for every request, including unauthenticated
	// orchestrator probes, so it runs ahead of any per-client rate limiting.
	// RecoverJSON is outermost so panics anywhere below this point still return
	// the gateway's JSON error envelope while inner middleware defers can run.
	r.Use(middleware.RecoverJSON(logger.Default()))
	// OTel middleware MUST come before logger.Middleware so any inbound
	// W3C traceparent is extracted into the request context, then the
	// logging layer reuses that trace ID for X-Request-ID. When no OTel
	// provider is configured this middleware is a cheap no-op (the
	// global propagator is the default no-op propagator).
	r.Use(gwotel.Middleware)
	r.Use(logger.Middleware) // inject trace ID + X-Request-ID header
	// SecurityHeaders applies baseline browser-hardening headers (Content-Security-Policy,
	// X-Content-Type-Options, X-Frame-Options, Referrer-Policy, and HSTS on TLS
	// connections) to every response. It is on the root stack rather than beside the
	// dashboard routes because the CSP has to reach the HTML page and the API responses
	// alike — the page is served from this same origin, so a policy that covered only
	// one of them would leave the other unprotected.
	// It must come before CORS so that security headers are present on all responses,
	// including preflight rejections and error responses.
	r.Use(middleware.SecurityHeaders)
	// A method the router cannot match is answered here rather than by chi's
	// bare 405 — see rejectUnknownMethod. It sits below SecurityHeaders so the
	// refusal carries the same hardening headers every other response does.
	r.Use(rejectUnknownMethod)
	// chi registers a route per method, so a HEAD reaches only the routes
	// declared for it: /health, /livez and /readyz are declared GET, and without
	// this every HEAD-based probe against them is a 405. GetHead re-dispatches a
	// HEAD that matches no HEAD route through the GET route instead, which is
	// the behaviour RFC 9110 §9.3.2 requires wherever GET is supported. It must
	// run above the routing tree — including the child router mounted below,
	// which inherits the rewritten route method — and the routes the gateway
	// registers for every method (see mountOpenAIRoutes) accept HEAD through
	// onlyMethod instead, so this leaves them alone.
	r.Use(chimiddleware.GetHead)

	// Orchestrator probes are mounted directly on the root router, ahead of
	// RealIPMiddleware/CORS/the per-client rate limiter (installed below on a
	// separate child router). Otherwise a traffic burst against /v1/* that
	// exhausts one source IP's rate-limit bucket would also 429 that same
	// IP's liveness probe (e.g. behind a shared load balancer), turning a
	// load spike into an orchestrator restart loop. See mountProbeRoutes for
	// why no probe may be rate limited, /readyz included.
	// CORS travels with the probes; the rate limiter does not. The exemption
	// above is about the limiter alone, and the two were only ever coupled
	// because both sat on the child router. Without this a probe answers a
	// preflight from its own method guard — a 405 whose Allow describes the
	// resource correctly and answers the wrong question, since what the browser
	// asked about was the Origin. That is the defect the CORS layer was moved
	// above the route to remove, and registering the probes for every method
	// reintroduced it for them by matching the OPTIONS the child router used to
	// receive. It also leaves a cross-origin GET of a probe without the headers
	// that would let a separate-origin dashboard read it.
	r.Group(func(probes chi.Router) {
		probes.Use(middleware.CORS(corsOrigins...))
		mountProbeRoutes(probes, gw, keyStore, cfgManager)
	})

	// Everything else sits behind RealIPMiddleware, CORS, and the per-client
	// rate limiter, mounted on a genuine child router rather than a
	// chi.Group: a chi Mux bakes its Use() stack into every route already
	// registered on it (and panics if Use() is called after a route is
	// added), and Group can only layer extra middleware onto a subset of
	// routes -- it can never exempt a route from middleware already
	// installed on the parent. Mounting a separate router is the only way to
	// keep /health, /livez, and /readyz out of this chain entirely.
	app := chi.NewRouter()
	// RealIPMiddleware resolves the client IP from X-Forwarded-For / X-Real-IP
	// only when the direct TCP peer is within a trusted-proxy CIDR, writing the
	// resolved host (no port) back into r.RemoteAddr. This replaces the
	// deprecated chi middleware.RealIP, which honored those headers
	// unconditionally and could be exploited by a caller that controlled them.
	app.Use(RealIPMiddleware(resolvedProxies))
	// AccessLog sits above CORS and the rate limiter so preflight rejections and
	// 429s are still logged, and below RealIPMiddleware so the resolved client IP
	// is recorded. It reads the trace ID from the X-Request-ID response header
	// that logger.Middleware set on the root stack above.
	app.Use(logger.AccessLog(logger.Default()))
	app.Use(middleware.CORS(corsOrigins...))
	// Optional per-IP rate limiting middleware.
	if rlStore != nil {
		app.Use(middleware.RateLimit(rlStore))
	}

	mountObservabilityRoutes(app, keyStore, sessionStore, masterKey)
	mountDashboardRoutes(app)
	mountAdminRoutes(app, gw, keyStore, sessionStore, cfgManager, logReader, logMaintainer, auditStore, masterKey)
	mountOpenAIRoutes(app, gw, keyStore, sessionStore, masterKey)
	r.Mount("/", app)

	return r
}

// mountDashboardRoutes serves the embedded operations dashboard.
//
// It is installed as the not-found handler rather than as a "/*" route so it
// cannot shadow anything: chi reaches it only once every registered pattern has
// failed to match, which keeps /v1/*, /admin/*, and the probes authoritative
// over their own paths no matter what the bundle happens to contain. That
// placement is also what makes the dashboard's client-side routes work — the
// gateway has no route for /overview or /keys, so a hard refresh lands here and
// receives the app shell.
//
// The handler itself declines anything that is not a GET or HEAD asking for
// text/html, so a mistyped API path still gets a 404 instead of an HTML page.
//
// The routes are unauthenticated because the bundle is: it is the login page
// and the static assets that render it, and every byte of gateway state it
// later displays is fetched from /admin, which authenticates on its own.
func mountDashboardRoutes(r chi.Router) {
	if !webui.Available() {
		// A binary built with a plain `go build` embeds the committed
		// placeholder instead of a bundle. Serving it is still better than a
		// 404 — the page names the command that produces the real thing — but
		// the operator should not have to discover that from a browser.
		logger.Default().Warn("dashboard bundle not embedded; serving placeholder (build with `make build`)")
	}
	r.NotFound(webui.Handler().ServeHTTP)
}

// ensureGateway returns gw if non-nil; otherwise builds a default fallback
// gateway from the registry.
func ensureGateway(gw *aigateway.Gateway, registry *providers.Registry) *aigateway.Gateway {
	if gw != nil {
		return gw
	}

	defaultTargets := make([]config.Target, 0, len(registry.List()))
	for _, name := range registry.List() {
		defaultTargets = append(defaultTargets, config.Target{VirtualKey: name})
	}
	cfg := config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets:  defaultTargets,
	}
	created, err := aigateway.New(cfg)
	if err != nil {
		logger.Default().Error("failed to build fallback gateway", "error", err)
		return nil
	}
	for _, name := range registry.List() {
		if p, ok := registry.Get(name); ok {
			created.RegisterProvider(p)
		}
	}
	return created
}

// mountProbeRoutes mounts the orchestrator health probes on r, ahead of the
// per-client middleware installed on the child router in NewRouter. All three
// are exempt from rate limiting.
//
// None of them may be shed. An orchestrator reads a failed probe as a verdict,
// not as backpressure: a refused /readyz removes the instance from service and
// a refused /livez restarts it. Rate limiting a probe therefore hands anyone who
// can reach the port — these routes are unauthenticated — a switch that takes
// the instance out of rotation, and /readyz is the worse half of that, because
// /livez keeps answering 200 and nothing ever restarts the process.
//
// /readyz was previously bounded by a process-global token bucket, on the
// reasoning that it fans out to Ping() calls against the key store and config
// manager on every request and that ceiling belongs to the stores rather than to
// any one caller. The ceiling is real; the instrument was wrong. The two goals
// only conflict while every request performs the fan-out, so the handler now
// caches its evaluated answer for readyzCacheTTL (internal/handler/health.go)
// and serves all callers from it. Store load is bounded by elapsed time — at
// most one evaluation per TTL regardless of caller count or source address —
// and no probe is ever refused.
// The probes go through onlyMethod like every other route rather than chi's
// per-method registration. Registered with r.Get they produced chi's bare 405:
// an empty body where every other path answers with the JSON error envelope,
// and an "Allow: GET" that omitted HEAD — which these routes do serve, and
// which orchestrators use. An Allow header naming fewer methods than the
// resource accepts is wrong in the same way the CORS preflight's was.
func mountProbeRoutes(r chi.Router, gw *aigateway.Gateway, store repository.Store, cfgManager handlers.ConfigManager) {
	r.Handle("/health", onlyMethod(http.MethodGet, handler.Health(gw)))
	// Split liveness/readiness probes for orchestrator rollout gating: /livez is
	// process-only, /readyz gates on config, store reachability, and providers.
	r.Handle("/livez", onlyMethod(http.MethodGet, handler.Livez()))
	r.Handle("/readyz", onlyMethod(http.MethodGet, handler.Readyz(gw, store, cfgManager)))
}

// mountObservabilityRoutes mounts the auth-gated /metrics, /debug/vars, and
// pprof routes.
//
// A dashboard session must authenticate here, not only on /admin/*: the web
// application holds a session token and never a raw API key, so gating these
// routes on the key chain alone made them unreadable from the UI that exists to
// display them. A session carries the scopes of the credential it was minted
// from, so accepting one grants nothing the same operator could not already
// reach with that credential directly.
//
// sessionStore must be the same instance mountAdminRoutes is handed, for the
// reason spelled out there: the middleware re-checks each session's source
// credential against keyStore, so a second store 401s every non-synthetic
// session.
//
// The group is split into two scope tiers rather than gated as one block,
// because a monitoring system scraping /metrics and an engineer pulling a heap
// dump are different actors and forcing them onto one credential tier is its
// own problem: a scraper's bearer sits unattended in a config file on every
// node that runs one, so the cheapest way to make it admin-tier is the reason
// not to. Production systems that authorize these paths at all draw the line in
// the same place — Kubernetes' built-in system:monitoring role grants /metrics,
// /healthz*, /livez*, and /readyz* and nothing under /debug, which needs an
// explicit non-resource grant or cluster-admin; Consul reads metrics with
// agent:read but pprof with operator:read; CockroachDB's debug pages want the
// admin role or VIEWDEBUG.
//
//   - /metrics is the monitoring tier (read-only or admin): counters and
//     histograms, the same class of state /admin/dashboard already serves to a
//     read-only credential.
//   - Everything under /debug is the operator tier (admin only): a heap or
//     goroutine profile is a memory image that can hold request bodies, keys,
//     and prompts, /debug/pprof/profile and /trace stop the world long enough
//     to be a denial-of-service primitive, and /debug/vars is grouped here
//     rather than with metrics because expvar publishes cmdline by default —
//     the process argv, not a counter.
//
// Scope is checked here and not left to the fact that pprof needs ENABLE_PPROF:
// that flag decides whether the routes exist, never who may call them.
//
// Both routes are read-only reporters, so both are narrowed to GET (and the HEAD
// onlyMethod pairs with it). They were registered for every method, which meant
// PUT /metrics returned the whole metric dump and POST /debug/vars returned
// expvar including the process command line — neither endpoint has a write
// surface to reach, but answering 200 to a method that means "write" describes
// the resource wrongly and leaves it the one place a 405 could never be raised.
// A Prometheus scrape is a GET (the exposition format defines no other method)
// and expvar is a GET; nothing in this repository calls either with anything else.
//
// pprof is deliberately not narrowed the same way and keeps its own per-method
// registration: /debug/pprof/symbol genuinely serves POST, which is how
// `go tool pprof` submits a batch of addresses to symbolize.
func mountObservabilityRoutes(r chi.Router, store repository.Store, sessions repository.SessionStore, masterKey string) {
	obsAuth := handlers.AuthMiddlewareWithSessions(store, sessions, masterKey)
	r.Group(func(r chi.Router) {
		r.Use(obsAuth)

		r.With(handlers.RequireScope(model.ScopeReadOnly, model.ScopeAdmin)).
			Handle("/metrics", onlyMethod(http.MethodGet, promhttp.Handler().ServeHTTP))

		// A nested group rather than a per-route .With(): the tier is a
		// property of the /debug prefix, so a route added here later is
		// admin-gated by construction instead of by remembering to say so.
		r.Group(func(r chi.Router) {
			r.Use(handlers.RequireScope(model.ScopeAdmin))
			r.Handle("/debug/vars", onlyMethod(http.MethodGet, expvar.Handler().ServeHTTP))
			mountPprofRoutes(r)
		})
	})
}

func mountAdminRoutes(
	r chi.Router,
	gw *aigateway.Gateway,
	keyStore repository.Store,
	sessionStore repository.SessionStore,
	cfgManager handlers.ConfigManager,
	logReader requestlog.Reader,
	logMaintainer requestlog.Maintainer,
	auditStore repository.AuditStore,
	masterKey string,
) {
	// The session-exchange handler authenticates the presented bearer itself
	// (see createSession) rather than through the AuthMiddlewareWithSessions
	// chain below, so it needs its own CredentialValidator. Building it here
	// reads the MASTER_KEY-derived state once; AuthMiddlewareWithSessions
	// below builds a second, functionally identical one internally for the same
	// (keyStore, masterKey) pair — see NewCredentialValidator's doc comment.
	credentials, _ := handlers.NewCredentialValidator(keyStore, masterKey)
	adminHandlers := &handlers.Handlers{
		Keys:        keyStore,
		Providers:   gw,
		Configs:     cfgManager,
		Logs:        logReader,
		LogAdmin:    logMaintainer,
		Sessions:    sessionStore,
		Audit:       auditStore,
		Credentials: credentials,
		// Read per request rather than snapshotted: an MCP server's readiness
		// changes while the process runs, and the handshake that decides the
		// first value has not finished when this router is built.
		MCPStatus: func() []handlers.MCPServerHealth { return mcpHealth(gw.Readiness()) },
		// The provider-catalog endpoint reports a per-provider model count,
		// including for providers with no credential, so it reads the live
		// catalog rather than the registered provider set.
		Catalog: gw.Catalog,
	}

	// Mounted outside AuthMiddlewareWithSessions: this is how a caller obtains
	// the credential that middleware validates. The handler authenticates the
	// presented bearer itself.
	//
	// It carries its own limiter rather than relying on the general per-IP one,
	// which shares a bucket with /v1 traffic and is removed entirely by
	// RATE_LIMIT_RPS=0. This is the only unauthenticated write path on the
	// gateway, so it cannot be left with a bound an operator can switch off
	// while tuning inference throughput.
	r.With(middleware.RateLimitKeyed(newSessionLimiter(), "admin_session")).
		Post("/admin/session", adminHandlers.CreateSessionHandler())

	r.Route("/admin", func(r chi.Router) {
		// AuthMiddlewareWithSessions must be handed the exact same sessionStore
		// instance as adminHandlers.Sessions above: the middleware re-checks
		// each session's source credential against keyStore on every request,
		// so a mismatched pair would 401 every non-synthetic session immediately.
		r.Use(handlers.AuthMiddlewareWithSessions(keyStore, sessionStore, masterKey))
		r.Use(middleware.MaxRequestBody(activeMaxRequestBytes(gw)))
		r.Mount("/", adminHandlers.Routes())
	})
}

// mcpHealth projects the gateway's MCP readiness onto the admin health shape.
//
// The failure reason is redacted here rather than at the registry that records
// it: the registry's copy is what reaches the server log, where an operator with
// shell access is entitled to the raw text, and this is the HTTP sink — the same
// place internal/apierror and the admin auth middleware apply redact.String.
//
// Redaction is best effort (see redact.String), which is why the reason travels
// only on the bearer-authenticated, scope-gated /admin/health and never on
// /readyz.
func mcpHealth(r aigateway.Readiness) []handlers.MCPServerHealth {
	if len(r.MCPServers) == 0 {
		return nil
	}
	out := make([]handlers.MCPServerHealth, 0, len(r.MCPServers))
	for _, s := range r.MCPServers {
		out = append(out, handlers.MCPServerHealth{
			Name:      s.Name,
			Ready:     s.Ready,
			Required:  s.Required,
			LastError: redact.String(s.LastError),
		})
	}
	return out
}

// Sign-in throttling for POST /admin/session.
//
// Every attempt costs a token, not only the failures. Today's credential is a
// 256-bit random key that cannot be guessed online at any rate, so the bound
// worth having is on the work the endpoint performs — and a flood of valid
// requests costs the same store lookups and hashing as invalid ones. Counting
// failures alone would leave that unbounded. (Passwords would change this:
// guessing becomes feasible, and the useful control becomes per-account
// lockout rather than per-address throttling.)
//
// The allowance is sized for a shared egress address, because on-premises
// deployments routinely put every operator behind one: a burst covers a whole
// team signing in at once, and the sustained rate covers the retries after it.
const (
	sessionAttemptBurst   = 20
	sessionAttemptsPerMin = 10
	// Bounds the per-address map. Eviction is least-recently-seen, so an
	// attacker rotating addresses evicts their own entries before an
	// operator's.
	sessionLimiterMaxKeys = 10_000
)

func newSessionLimiter() *ratelimit.Store {
	return ratelimit.NewStoreWithMax(sessionAttemptsPerMin/60.0, sessionAttemptBurst, sessionLimiterMaxKeys)
}

func mountOpenAIRoutes(r chi.Router, gw *aigateway.Gateway, store repository.Store, sessionStore repository.SessionStore, masterKey string) {
	// /v1/* also accepts a dashboard session bearer: the dashboard Playground
	// sends the operator's dashboard credential straight through to
	// /v1/chat/completions, and rejecting sessions here would break it. This
	// does not change which credentials may invoke inference — a session
	// simply carries the scopes of the credential it was minted from.
	auth := middleware.ProxyAuthWithSessions(store, sessionStore, masterKey)

	// Auth runs ahead of the per-route method check, so an unauthenticated
	// request with the wrong method is answered 401 and never learns that the
	// path exists or which methods it serves. That ordering is deliberate: an
	// Allow header is a description of a resource, and describing resources to
	// callers who have not authenticated is how a probe maps the surface. A
	// caller who authenticates gets the 405 and its Allow header.
	r.Group(func(r chi.Router) {
		r.Use(auth)
		r.Use(middleware.MaxRequestBody(activeMaxRequestBytes(gw)))
		// Each natively-handled route is registered for every method, serving
		// its own method and refusing the rest. Registering only the method a
		// route serves leaves every other method matching the /v1/* pass-through
		// below, which forwards upstream with the operator's credential and so
		// past every guardrail, budget, circuit breaker, concurrency limit and
		// the targets allowlist. A wrong method on a route the gateway owns is a
		// client mistake, not a pass-through candidate.
		r.Handle("/v1/models", onlyMethod(http.MethodGet, handler.Models(gw)))
		r.Handle("/v1/capabilities", onlyMethod(http.MethodGet, handler.Capabilities(gw)))
		r.Handle("/v1/chat/completions", onlyMethod(http.MethodPost, handler.ChatCompletions(gw)))

		// Legacy text completions.
		r.Handle("/v1/completions", onlyMethod(http.MethodPost, handler.Completions(gw)))

		// Embeddings endpoint.
		r.Handle("/v1/embeddings", onlyMethod(http.MethodPost, handler.Embeddings(gw)))

		// Image generation endpoint.
		r.Handle("/v1/images/generations", onlyMethod(http.MethodPost, handler.Images(gw)))
		r.Handle("/v1/rerank", onlyMethod(http.MethodPost, handler.Rerank(gw)))
		r.Handle("/v1/moderations", onlyMethod(http.MethodPost, handler.Moderations(gw)))

		// Text-to-speech: a small JSON request (input capped at 4096 chars,
		// enforced by handler.Speech), so it belongs here under the shared body
		// limit — unlike the multipart audio upload routes below. The response is
		// raw audio bytes, streamed by the handler.
		r.Handle("/v1/audio/speech", onlyMethod(http.MethodPost, handler.Speech(gw)))

		// Proxy pass-through for unhandled /v1/* endpoints. It is handed the
		// gateway, not the registry: resolving a body's model through the
		// registry is a scan of each provider's advisory SupportsModel, which
		// most answer `return true`, so an unowned model forwarded the request
		// body to whichever provider registered first. The gateway answers from
		// the routing index instead — the same authority the natively handled
		// surfaces use.
		r.HandleFunc("/v1/*", proxy.Handler(gw))
	})

	// Audio transcription/translation take a multipart file upload, larger than
	// the JSON surfaces: OpenAI's whisper accepts 25 MiB. These two routes sit in
	// a sibling group WITHOUT the shared MaxRequestBody middleware — the handler
	// applies its own (higher) MaxBytesReader before the multipart parser reads
	// the body, since an inner reader cannot loosen the /v1/* group's 10 MiB cap.
	// As fully static paths they win over the /v1/* pass-through above. Speech
	// (TTS) is JSON and stays in the group above.
	r.Group(func(r chi.Router) {
		r.Use(auth)
		r.Handle("/v1/audio/transcriptions", onlyMethod(http.MethodPost, handler.Transcriptions(gw, false)))
		r.Handle("/v1/audio/translations", onlyMethod(http.MethodPost, handler.Transcriptions(gw, true)))
	})

	// Files + Batch pass-through to the single configured batch target. Like the
	// audio uploads a file upload (POST /v1/files) can be far larger than the JSON
	// surfaces, so these sit outside the shared body limit and stream straight
	// through. Every method the two APIs use is forwarded (GET list/retrieve, POST
	// upload/create/cancel, DELETE), so — unlike the routes above — no onlyMethod
	// guard: this surface IS the pass-through to that backend, and an unexpected
	// method is the upstream's 405 to give. As static/prefixed paths they win over
	// the /v1/* proxy above. Off (501) when no batch_target is configured.
	r.Group(func(r chi.Router) {
		r.Use(auth)
		batch := proxy.BatchHandler(gw)
		r.Handle("/v1/files", batch)
		r.Handle("/v1/files/*", batch)
		r.Handle("/v1/batches", batch)
		r.Handle("/v1/batches/*", batch)
	})

	// Responses. POST /v1/responses is model-routed and GOVERNED + PRICED (usage
	// is teed off the response/stream); it sits outside the shared body limit
	// because a Responses input can be up to ~10 MiB and the forward streams it.
	// The stateful id sub-routes (retrieve/delete/cancel/input_items) carry no
	// model and pin to responses_target. Static/prefixed paths win over /v1/*.
	r.Group(func(r chi.Router) {
		r.Use(auth)
		r.Handle("/v1/responses", onlyMethod(http.MethodPost, proxy.ResponsesCreate(gw)))
		r.Handle("/v1/responses/*", proxy.ResponsesIDs(gw))
	})
}
