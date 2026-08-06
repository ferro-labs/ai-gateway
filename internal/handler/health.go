package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
)

// readyzPingTimeout bounds how long Readyz waits for all backing-store pings
// combined before reporting the gateway not ready.
const readyzPingTimeout = 2 * time.Second

// readyzCacheTTL is how long one evaluated readiness answer is served to every
// caller before the backing stores are consulted again.
//
// This is what bounds the Ping() fan-out /readyz aims at the key store and the
// config manager. The endpoint is unauthenticated, so that fan-out has to be
// capped somehow — but it must never be capped by refusing requests. An
// orchestrator reads a failed readiness probe as "remove this instance from
// service", so shedding probes hands any unauthenticated caller a switch that
// takes the instance out of rotation; and because /livez keeps answering 200,
// nothing restarts it either. Rate limiting is the wrong instrument here for
// the same reason a load shedder is the wrong instrument in front of a smoke
// detector.
//
// Caching the *result* separates the two concerns that were in conflict. How
// often we answer is unbounded — every probe gets a real answer. How often we
// touch the stores is bounded by elapsed time: at most one evaluation per TTL,
// whether one caller asks or a million do. The ceiling is now a property of the
// stores, expressed in the units the stores actually care about.
//
// One second is chosen against the probe cadence on the other side. A
// Kubernetes readiness probe's periodSeconds is 10 by default and cannot be set
// below 1, so a one-second TTL is at or under the fastest cadence any probe can
// run at: an answer is never older than the interval between two probes, and a
// genuine transition — in either direction — is reported on the very next one.
// Going longer would start costing detection latency; going shorter would buy
// nothing a probe could observe while raising the floor on store load.
const readyzCacheTTL = time.Second

// Pinger is the minimal reachability probe Readyz applies to each backing store.
// The API key store and the config manager both satisfy it.
type Pinger interface {
	Ping(context.Context) error
}

// providerCircuit is the per-provider circuit state reported by Readyz.
type providerCircuit struct {
	Name    string `json:"name"`
	Circuit string `json:"circuit"`
}

// targetState is the per-configured-target routability reported by Readyz.
//
// It carries no reason string on purpose: /readyz is unauthenticated, and the
// two ways a target goes unroutable — no provider registered under that name, or
// an open circuit — are already distinguishable from the providers list without
// naming a credential or a host.
type targetState struct {
	Name     string `json:"name"`
	Routable bool   `json:"routable"`
}

// mcpServerState is the per-MCP-server state reported by Readyz.
//
// LastError is deliberately absent: /readyz is unauthenticated and an MCP
// initialization failure can quote a server URL, an authorization header, or a
// full subprocess command line. The reason is logged server-side and served
// on the bearer-authenticated GET /admin/health instead.
type mcpServerState struct {
	Name     string `json:"name"`
	Ready    bool   `json:"ready"`
	Required bool   `json:"required"`
}

// Livez handles GET /livez. It reports process liveness only, performing no
// dependency checks, so it always returns 200. Orchestrators use it to decide
// whether to restart the process.
func Livez() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

// readyzSnapshot is one evaluated readiness answer. It is shared by every
// caller served from the same cache entry, so it is only ever read after the
// evaluation that produced it returns — nothing mutates it afterwards.
type readyzSnapshot struct {
	// reason is empty when the gateway is ready; otherwise it names the failed
	// check and body is nil.
	reason string
	body   map[string]any
}

// readyzCache holds the most recent readiness answer and hands it to every
// caller that arrives within readyzCacheTTL of it. See that constant for why
// /readyz bounds its dependency load this way instead of by rate limiting.
//
// The mutex is held across the evaluation, which is deliberate: callers that
// arrive while one is in flight wait for it and are then served from its
// result, rather than each starting a redundant fan-out of their own. That
// makes a burst cost exactly one round of pings, not one per caller.
type readyzCache struct {
	ttl  time.Duration
	now  func() time.Time
	eval func(context.Context) readyzSnapshot

	mu          sync.Mutex
	evaluatedAt time.Time // zero until the first evaluation completes
	snapshot    readyzSnapshot
}

func (c *readyzCache) get(ctx context.Context) readyzSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.evaluatedAt.IsZero() && c.now().Sub(c.evaluatedAt) < c.ttl {
		return c.snapshot
	}
	c.snapshot = c.eval(ctx)
	c.evaluatedAt = c.now()
	return c.snapshot
}

// Readyz handles GET /readyz. It reports whether the gateway can serve traffic:
// config must be loaded, every pinger must be reachable within
// readyzPingTimeout, and at least one provider must have a non-open circuit. It
// returns 200 with the readiness snapshot when ready, otherwise 503 with a
// reason naming the failed check.
//
// Every request is answered — a readiness probe is never refused for volume.
// The backing-store pings behind the answer are consulted at most once per
// readyzCacheTTL no matter how many callers ask.
func Readyz(gw *aigateway.Gateway, pingers ...Pinger) http.HandlerFunc {
	return readyz(gw, pingers, time.Now)
}

// readyz is Readyz with the clock injected so tests can drive cache staleness
// without sleeping.
func readyz(gw *aigateway.Gateway, pingers []Pinger, now func() time.Time) http.HandlerFunc {
	cache := &readyzCache{
		ttl: readyzCacheTTL,
		now: now,
		eval: func(ctx context.Context) readyzSnapshot {
			return evaluateReadiness(ctx, gw, pingers)
		},
	}

	return func(w http.ResponseWriter, r *http.Request) {
		// context.WithoutCancel keeps the request's values — the logger and
		// trace ID travel with it — while dropping its cancellation. One
		// caller's evaluation is served to every other caller waiting on it, so
		// a client that hangs up mid-probe (an orchestrator's timeoutSeconds
		// defaults to 1, below readyzPingTimeout) must not be able to abort the
		// answer everyone else is about to receive.
		snapshot := cache.get(context.WithoutCancel(r.Context()))
		if snapshot.reason != "" {
			writeNotReady(w, snapshot.reason)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snapshot.body)
	}
}

// evaluateReadiness runs the readiness checks once and returns the answer to
// cache. Failures are logged here rather than at write time, so the log records
// one line per real evaluation instead of one per probe.
func evaluateReadiness(ctx context.Context, gw *aigateway.Gateway, pingers []Pinger) readyzSnapshot {
	if gw == nil {
		return readyzSnapshot{reason: "gateway not configured"}
	}

	pingCtx, cancel := context.WithTimeout(ctx, readyzPingTimeout)
	defer cancel()
	for _, p := range pingers {
		if p == nil {
			continue
		}
		if err := p.Ping(pingCtx); err != nil {
			logger.Ctx(ctx).Error("readyz store ping failed", "error", err)
			return readyzSnapshot{reason: "store unreachable"}
		}
	}

	readiness := gw.Readiness()
	if !readiness.Ready {
		// Not "no providers": the process can hold a set of perfectly healthy
		// providers and still serve nothing, because no configured target names
		// any of them. Routability is what decides whether a request can
		// succeed.
		return readyzSnapshot{reason: "no routable targets"}
	}

	// Only a server explicitly marked `required` gates readiness. Every
	// server's state is reported either way, so an operator can watch MCP
	// health without a dead optional server pulling the instance out of
	// rotation and stopping traffic that never touches a tool.
	if down := requiredMCPDown(readiness); down != nil {
		logger.Ctx(ctx).Error("readyz required MCP server unavailable",
			"server", down.Name, "error", down.LastError)
		return readyzSnapshot{reason: "required mcp server unavailable"}
	}

	// Targets are reported on a ready response too, for the same reason MCP
	// state is: an instance serving on two of three targets is degraded, and
	// naming the third here is what lets an operator find the typo without the
	// instance having to leave rotation to tell them.
	body := map[string]any{
		"status":    "ready",
		"providers": circuitsFromReadiness(readiness),
		"targets":   targetsFromReadiness(readiness),
	}
	if mcpStates := mcpFromReadiness(readiness); len(mcpStates) > 0 {
		body["mcp_servers"] = mcpStates
	}
	return readyzSnapshot{body: body}
}

// requiredMCPDown returns the first required MCP server that is not ready, or
// nil when every required server is up.
func requiredMCPDown(r aigateway.Readiness) *aigateway.MCPServerReadiness {
	for i := range r.MCPServers {
		if r.MCPServers[i].Required && !r.MCPServers[i].Ready {
			return &r.MCPServers[i]
		}
	}
	return nil
}

// targetsFromReadiness projects the target half of the readiness snapshot onto
// the JSON-tagged response shape.
func targetsFromReadiness(r aigateway.Readiness) []targetState {
	out := make([]targetState, 0, len(r.Targets))
	for _, t := range r.Targets {
		out = append(out, targetState{Name: t.Name, Routable: t.Routable})
	}
	return out
}

// mcpFromReadiness projects the MCP half of the readiness snapshot onto the
// JSON-tagged response shape, dropping LastError.
func mcpFromReadiness(r aigateway.Readiness) []mcpServerState {
	out := make([]mcpServerState, 0, len(r.MCPServers))
	for _, s := range r.MCPServers {
		out = append(out, mcpServerState{Name: s.Name, Ready: s.Ready, Required: s.Required})
	}
	return out
}

// writeNotReady emits a 503 with a JSON body naming the failed readiness check.
// /readyz is unauthenticated, so reason must stay a fixed, generic string —
// never the underlying error, which can carry a DSN, host, or credential.
// Callers log the real error server-side before calling this.
func writeNotReady(w http.ResponseWriter, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "not_ready",
		"reason": reason,
	})
}

// circuitsFromReadiness projects the gateway readiness snapshot onto the
// JSON-tagged response shape.
func circuitsFromReadiness(r aigateway.Readiness) []providerCircuit {
	out := make([]providerCircuit, 0, len(r.Providers))
	for _, p := range r.Providers {
		out = append(out, providerCircuit{Name: p.Name, Circuit: p.Circuit})
	}
	return out
}
