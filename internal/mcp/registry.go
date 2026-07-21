package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	rtrace "runtime/trace"
	"sync"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/metrics"
	gwotel "github.com/ferro-labs/ai-gateway/internal/otel"
	"github.com/ferro-labs/ai-gateway/observability"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// errTransportExited is the cause recorded when a server is marked unready
// because its transport died rather than because a call or handshake failed.
var errTransportExited = errors.New("mcp: server transport exited")

const (
	// transportProbeInterval is how often a stdio server whose stderr reached
	// EOF is re-probed while it keeps answering. It runs only for a server that
	// closed stderr, so the steady-state cost is a ping a second on a local pipe
	// for a server behaving unusually — cheap enough not to warrant a backoff.
	transportProbeInterval = time.Second

	// transportProbeTimeout bounds a single confirmation ping. A dead transport
	// fails immediately; this only limits how long a wedged one is waited on
	// before the next attempt.
	transportProbeTimeout = 5 * time.Second
)

// Registry manages registered MCP servers and the tools they expose.
// All methods are safe for concurrent use.
//
// Conflict policy: when two servers advertise the same tool name the
// first-registered server wins. Both toolMap and AllTools honour this policy
// so that FindToolServer and AllTools always return consistent results.
type Registry struct {
	mu          sync.RWMutex
	servers     map[string]*serverEntry // server name => entry
	toolMap     map[string]string       // tool name => server name (O(1) lookup)
	regOrder    []string                // server names in registration order
	serverIndex map[string]int          // server name => position in regOrder

	// Retirement lifecycle. A request snapshots the registry at entry and uses
	// it after releasing the gateway lock, so a config reload that closed the
	// old registry immediately would terminate a stdio subprocess underneath an
	// in-flight tool call. Holders are counted, and teardown waits for the last
	// one. Mirrors the plugin.Manager lifecycle.
	lifecycleMu sync.Mutex
	lifecycle   *sync.Cond
	active      int
	closed      bool

	// stopCtx is cancelled by teardown. Transport watchers select on it so a
	// closed registry leaves no goroutine behind, and it parents their probes so
	// one already in flight is cut short rather than run to its timeout.
	stopCtx  context.Context
	stopFunc context.CancelFunc
}

// serverEntry holds the live state for one registered MCP server.
type serverEntry struct {
	config       ServerConfig
	client       mcpClient
	tools        []Tool
	ready        bool  // true once Initialize + ListTools have succeeded
	initializing bool  // true while initServer goroutine is running for this entry
	initErr      error // last initialization error; nil when ready
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	stopCtx, stopFunc := context.WithCancel(context.Background())
	return &Registry{
		servers:     make(map[string]*serverEntry),
		toolMap:     make(map[string]string),
		serverIndex: make(map[string]int),
		stopCtx:     stopCtx,
		stopFunc:    stopFunc,
	}
}

// gaugeOwners records which Registry most recently registered each server name.
//
// metrics.MCPServerUp is labelled by server name alone, so every Registry writes
// the same process-wide series. A configuration reload builds the replacement
// Registry while the previous one is still draining in-flight requests, and the
// retired one's teardown would otherwise write 0 over the replacement's 1 —
// permanently, because InitializeAll never re-initializes an already-ready
// server and so nothing writes 1 again. Registration claims the label and
// teardown writes only while that claim still holds: the same ownership
// discipline markUnready applies to an entry, applied one level up to the label.
var gaugeOwners struct {
	sync.Mutex
	owner map[string]*Registry
}

// claimServerUp takes ownership of a server's gauge series and publishes 0, so
// a server that never initializes is visibly down rather than absent.
func (r *Registry) claimServerUp(name string) {
	gaugeOwners.Lock()
	if gaugeOwners.owner == nil {
		gaugeOwners.owner = make(map[string]*Registry)
	}
	gaugeOwners.owner[name] = r
	gaugeOwners.Unlock()

	metrics.MCPServerUp.WithLabelValues(name).Set(0)
}

// setServerUp writes value only while this Registry still owns the label.
func (r *Registry) setServerUp(name string, value float64) {
	gaugeOwners.Lock()
	owned := gaugeOwners.owner[name] == r
	gaugeOwners.Unlock()
	if owned {
		metrics.MCPServerUp.WithLabelValues(name).Set(value)
	}
}

// releaseServerUp drops the series on teardown when this Registry still owns it.
// Deleting rather than zeroing is what stops a server removed from the
// configuration from lingering at 0 for the life of the process.
func (r *Registry) releaseServerUp(name string) {
	gaugeOwners.Lock()
	defer gaugeOwners.Unlock()
	if gaugeOwners.owner[name] == r {
		delete(gaugeOwners.owner, name)
		metrics.MCPServerUp.DeleteLabelValues(name)
	}
}

// RegisterConfig stores an MCP server configuration and creates its transport
// client without performing any I/O. Call InitializeAll in a background
// goroutine after gateway.New() returns so the first LLM request is never
// blocked by MCP cold-start latency.
//
// Transport selection: when cfg.Command is non-empty a stdio client is created
// (the subprocess is started immediately); otherwise an HTTP client is created.
//
// Re-registering a server with the same Name closes the old client, preserves
// the original registration order (and therefore its tool-conflict priority),
// and removes stale tool→server mappings so FindToolServer never routes to
// stale state.
func (r *Registry) RegisterConfig(cfg ServerConfig) {
	var client mcpClient
	if cfg.Command != "" {
		client = newStdioClient(cfg.Name, cfg.Command, cfg.Args, cfg.Env)
	} else {
		timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		client = NewClient(cfg.URL, cfg.Headers, timeout)
	}

	r.mu.Lock()
	if old, ok := r.servers[cfg.Name]; ok {
		// Clean up only the toolMap entries that this server owned.
		for _, t := range old.tools {
			if r.toolMap[t.Name] == cfg.Name {
				delete(r.toolMap, t.Name)
			}
		}
		// Close the old client (no-op for HTTP; terminates subprocess for stdio).
		// A failure here means a subprocess may have survived re-registration,
		// which is the leak this call exists to prevent — never silent.
		if old.client != nil {
			if err := old.client.Close(); err != nil {
				slog.Warn("mcp: failed to close replaced server client",
					"server", cfg.Name, "error", err)
			}
		}
		// Registration order and serverIndex are preserved on re-registration.
	} else {
		// First-time registration: assign a position in regOrder.
		r.serverIndex[cfg.Name] = len(r.regOrder)
		r.regOrder = append(r.regOrder, cfg.Name)
	}
	r.servers[cfg.Name] = &serverEntry{
		config: cfg,
		client: client,
	}
	r.mu.Unlock()

	// Publish the gauge immediately so a server that never initializes is
	// visibly down rather than absent from /metrics entirely.
	r.claimServerUp(cfg.Name)

	// Watch for the transport dying under us. Started after the entry is stored
	// so the watcher can always resolve what it is reporting on, and keyed on
	// this exact client so a re-registration that replaces the transport is not
	// clobbered by the previous one's death notice.
	if w, ok := client.(interface{ Exited() <-chan struct{} }); ok {
		if exited := w.Exited(); exited != nil {
			go r.watchTransport(cfg.Name, client, exited)
		}
	}
}

// RegisterFailed records a server the operator configured but that could not be
// built at all — a bad env reference in its headers or environment, resolved
// before any transport exists. It is stored unready with cause as its last
// error and takes its place in registration order like any other server.
//
// Without it such a server is invisible: Status reports what the registry holds,
// so a server that was skipped before RegisterConfig cannot appear in the
// readiness body, and one marked `required` would leave /readyz reporting ready
// while the server it depends on was never even attempted. Registering the
// failure keeps the registry the single source of truth for readiness, and the
// gauge is published here too so the server reads 0 on /metrics rather than
// being missing.
//
// The entry carries no client. Nothing can initialize it, and InitializeAll
// skips it on exactly that basis, so cause survives as the reported reason
// until the next configuration reload rebuilds the registry.
func (r *Registry) RegisterFailed(cfg ServerConfig, cause error) {
	r.mu.Lock()
	if _, ok := r.servers[cfg.Name]; !ok {
		r.serverIndex[cfg.Name] = len(r.regOrder)
		r.regOrder = append(r.regOrder, cfg.Name)
	}
	r.servers[cfg.Name] = &serverEntry{config: cfg, initErr: cause}
	r.mu.Unlock()

	r.claimServerUp(cfg.Name)
}

// watchTransport turns a stdio server's stderr EOF from a verdict into a
// trigger.
//
// EOF is not proof of death. The gateway holds only the read end of the pipe,
// so a live server that closes its own fd 2 — or hands work to a child that
// does — produces a genuine EOF while continuing to answer calls. Acting on it
// directly withdrew healthy servers, and with `required: true` that is a 503 for
// a server that was never down.
//
// So the report is confirmed with a ping before anything is withdrawn: a live
// server answers, a dead transport fails immediately because stdout EOF already
// closed it. A server that answers is left alone and re-probed, which also
// closes the gap where a server closing stderr at startup consumed its one
// notification and was exempt from death detection from then on.
//
// The loop costs one ping per interval and only for a server that closed stderr
// at all; a server holding stderr open — the ordinary case — never reaches it.
func (r *Registry) watchTransport(name string, client mcpClient, exited <-chan struct{}) {
	select {
	case <-exited:
	case <-r.stopCtx.Done():
		return
	}

	probe, ok := client.(pinger)
	if !ok {
		// No way to confirm. Report the death rather than leave a transport that
		// may well be gone advertised until the next reload.
		r.markUnready(name, client, errTransportExited)
		return
	}

	ticker := time.NewTicker(transportProbeInterval)
	defer ticker.Stop()
	for {
		holds, ready := r.probeState(name, client)
		// Teardown detaches the client, and re-registration replaces it. Either
		// way this transport's fate stopped being ours to report.
		if !holds {
			return
		}

		// Probe only a server the registry has published as ready. Until then
		// the handshake owns the transport — the client library tracks its own
		// initialized state without synchronisation, so a concurrent ping races
		// it (mark3labs/mcp-go#935) — and there is nothing to withdraw in any
		// case. Observing ready under r.mu orders this goroutine after that
		// handshake, which is what makes the probe safe; keep that ordering
		// until the upstream field is synchronised.
		if ready {
			ctx, cancel := context.WithTimeout(r.stopCtx, transportProbeTimeout)
			err := probe.Ping(ctx)
			cancel()
			if isTransportDead(err) {
				r.markUnready(name, client, fmt.Errorf("%w: %w", errTransportExited, err))
				return
			}
		}

		select {
		case <-ticker.C:
		case <-r.stopCtx.Done():
			return
		}
	}
}

// probeState reports whether the named server is still served by this exact
// transport, and whether the registry currently considers it ready.
func (r *Registry) probeState(name string, client mcpClient) (holds, ready bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.servers[name]
	if !ok || entry.client != client {
		return false, false
	}
	return true, entry.ready
}

// markUnready clears the ready bit for a server whose transport is gone, so
// that AllTools stops advertising its tools and FindToolServer stops resolving
// them. cause is retained as the server's last error and surfaced by Status.
//
// client identifies which transport is being reported dead. A registry that has
// since replaced or detached that transport ignores the report: without the
// check, a stdio server's death notice could arrive after a config reload had
// already spawned a healthy replacement under the same name and permanently
// mark the replacement down.
//
// Recovery is deliberately not attempted here. Re-registering the server
// through RegisterConfig and re-running InitializeAll already rebuilds the
// transport and re-indexes its tools; nothing further is needed to bring a
// server back, and restart-with-backoff is not part of this change.
func (r *Registry) markUnready(name string, client mcpClient, cause error) {
	r.mu.Lock()
	entry, ok := r.servers[name]
	if !ok || entry.client != client || !entry.ready {
		// Not this transport, or already down — nothing to clear, and no log.
		r.mu.Unlock()
		return
	}
	entry.ready = false
	entry.initErr = cause
	r.mu.Unlock()

	r.setServerUp(name, 0)
	slog.Warn("mcp: server is no longer available; its tools are withdrawn",
		"server", name, "error", cause)
}

// InitializeAll performs the MCP handshake and tool discovery for every
// registered server that is not yet ready. It is idempotent and safe to call
// concurrently: each server is initialized at most once at a time even when
// multiple goroutines call InitializeAll simultaneously. Errors are reported
// via logErr (never returned) so the caller can log them without blocking.
func (r *Registry) InitializeAll(ctx context.Context, logErr func(name string, err error)) {
	ctx, task := rtrace.NewTask(ctx, "mcp.initialize_all")
	defer task.End()

	r.mu.RLock()
	names := make([]string, len(r.regOrder))
	copy(names, r.regOrder)
	r.mu.RUnlock()

	var wg sync.WaitGroup
	for _, name := range names {
		// Fast-path read: skip servers that are already done or in progress.
		// A nil client is not something a handshake can fix: either the registry
		// was torn down, or the server was recorded by RegisterFailed and never
		// had a transport. Skipping keeps its recorded reason intact instead of
		// replacing it with a generic initialization error.
		r.mu.RLock()
		entry, ok := r.servers[name]
		skip := !ok || entry.ready || entry.initializing || entry.client == nil
		r.mu.RUnlock()
		if skip {
			continue
		}

		// Slow path: re-check under write lock before setting initializing flag.
		// This prevents two concurrent InitializeAll callers from both spawning
		// initServer goroutines for the same server.
		r.mu.Lock()
		entry, ok = r.servers[name]
		if !ok || entry.ready || entry.initializing || entry.client == nil {
			r.mu.Unlock()
			continue
		}
		entry.initializing = true
		r.mu.Unlock()

		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			// Hold the registry for the handshake. Without it Close can retire the
			// registry mid-initialisation and return while transports are still
			// being spoken to, and the entry could be published ready after
			// closeClients had already detached its client.
			release := r.Acquire()
			defer release()
			if err := r.initServer(ctx, n); err != nil && logErr != nil {
				logErr(n, err)
			}
		}(name)
	}
	wg.Wait()
}

// initServer performs the Initialize + ListTools handshake for a single server
// and indexes its tools. It applies the AllowedTools filter if configured.
func (r *Registry) initServer(ctx context.Context, name string) error {
	// Startup-path span. Nothing above opens one — initialization runs in a
	// background goroutine with no request to parent it — so this is the root
	// of the trace for one server's cold start.
	ctx, span := mcpTracer().Start(ctx, "mcp.init_server",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String(observability.AttrFerroMCPServer, name)),
	)
	defer span.End()

	// Every failure below is counted and recorded once, here, rather than at
	// each return: three exits reporting the same outcome three ways is how
	// they drift apart.
	var initErr error
	defer func() {
		if initErr != nil {
			metrics.MCPServerInitFailures.WithLabelValues(name).Inc()
			gwotel.RecordSpanError(span, initErr)
		}
	}()

	// Capture the client under the lock and use that copy for the whole
	// handshake. A concurrent Close detaches entry.client, so re-reading the
	// field across the unlocked I/O below would nil-deref mid-initialisation.
	r.mu.RLock()
	entry, ok := r.servers[name]
	var client mcpClient
	if ok {
		client = entry.client
	}
	r.mu.RUnlock()
	if !ok {
		initErr = fmt.Errorf("mcp: server %q not registered", name)
		return initErr
	}
	if client == nil {
		// The registry was closed while this initialisation was queued. Stop
		// rather than resurrecting a retired server.
		r.mu.Lock()
		entry.initializing = false
		r.mu.Unlock()
		initErr = fmt.Errorf("mcp: server %q closed before initialization", name)
		return initErr
	}

	var err error
	func() {
		hsCtx, hsSpan := mcpTracer().Start(ctx, "mcp.initialize", trace.WithSpanKind(trace.SpanKindClient))
		defer hsSpan.End()
		rtrace.WithRegion(hsCtx, "mcp.init_server.initialize", func() {
			_, err = client.Initialize(hsCtx)
		})
		if err != nil {
			gwotel.RecordSpanError(hsSpan, err)
		}
	}()
	if err != nil {
		r.mu.Lock()
		entry.initErr = err
		entry.initializing = false
		r.mu.Unlock()
		initErr = fmt.Errorf("mcp init %s: %w", name, err)
		return initErr
	}

	var tools []Tool
	func() {
		ltCtx, ltSpan := mcpTracer().Start(ctx, "mcp.list_tools", trace.WithSpanKind(trace.SpanKindClient))
		defer ltSpan.End()
		rtrace.WithRegion(ltCtx, "mcp.init_server.list_tools", func() {
			tools, err = client.ListTools(ltCtx)
		})
		if err != nil {
			gwotel.RecordSpanError(ltSpan, err)
		} else {
			ltSpan.SetAttributes(attribute.Int("mcp.tools.count", len(tools)))
		}
	}()
	if err != nil {
		r.mu.Lock()
		entry.initErr = err
		entry.initializing = false
		r.mu.Unlock()
		initErr = fmt.Errorf("mcp list tools %s: %w", name, err)
		return initErr
	}

	// Apply allowed-tools filter when an explicit list is provided.
	if len(entry.config.AllowedTools) > 0 {
		allowed := make(map[string]bool, len(entry.config.AllowedTools))
		for _, t := range entry.config.AllowedTools {
			allowed[t] = true
		}
		filtered := tools[:0]
		for _, t := range tools {
			if allowed[t.Name] {
				filtered = append(filtered, t)
			}
		}
		tools = filtered
	}

	r.mu.Lock()
	// Remove stale toolMap entries from any previous indexing of this server.
	// This handles re-registration — old tool→server mappings that are no
	// longer valid must not linger in the map.
	for _, t := range entry.tools {
		if r.toolMap[t.Name] == name {
			delete(r.toolMap, t.Name)
		}
	}
	entry.tools = tools
	entry.ready = true
	entry.initializing = false
	entry.initErr = nil
	// Populate toolMap using a first-registered-wins conflict policy.
	// If the slot is vacant this server claims it. If another server already
	// holds the slot we override only when our registration index is lower
	// (i.e. we were registered earlier and therefore have higher priority).
	ourIdx := r.serverIndex[name]
	for _, t := range tools {
		if existing, ok := r.toolMap[t.Name]; !ok {
			r.toolMap[t.Name] = name
		} else if existing != name && r.serverIndex[existing] > ourIdx {
			// We have higher priority; take over the mapping.
			r.toolMap[t.Name] = name
		}
		// else: existing server has equal-or-higher priority; keep it.
	}
	r.mu.Unlock()

	r.setServerUp(name, 1)

	return nil
}

// ServerStatus is a point-in-time view of one registered MCP server.
//
// It is not public API — internal/mcp is an internal package, so this type is
// reachable only from within the module.
type ServerStatus struct {
	// Name is the server's configured name.
	Name string
	// Ready reports whether the server completed initialization and its
	// transport is still live.
	Ready bool
	// Required mirrors ServerConfig.Required — whether this server's
	// availability gates gateway readiness.
	Required bool
	// LastError is the most recent initialization or transport failure, empty
	// when the server is ready. It can quote a URL, host, or command line, so
	// it belongs in server-side logs and never in an unauthenticated response.
	LastError string
}

// Status returns a snapshot of every registered server in registration order.
//
// It is the read side of the state initServer and markUnready maintain: without
// it a failed handshake left entry.initErr set and unreachable, so an operator
// could see that a server was down but never why.
func (r *Registry) Status() []ServerStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]ServerStatus, 0, len(r.regOrder))
	for _, name := range r.regOrder {
		entry, ok := r.servers[name]
		if !ok {
			continue
		}
		st := ServerStatus{
			Name:     name,
			Ready:    entry.ready,
			Required: entry.config.Required,
		}
		if entry.initErr != nil {
			st.LastError = entry.initErr.Error()
		}
		out = append(out, st)
	}
	return out
}

// FindToolServer returns the transport client responsible for the named tool.
// Returns (nil, false) when no ready server exposes the tool.
func (r *Registry) FindToolServer(toolName string) (mcpClient, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	serverName, ok := r.toolMap[toolName]
	if !ok {
		return nil, false
	}
	entry, ok := r.servers[serverName]
	if !ok || !entry.ready {
		return nil, false
	}
	return entry.client, true
}

// Owns reports whether a ready server exposes the named tool.
//
// This is the ownership boundary between MCP tools and tools the caller
// supplied in their own request. A caller posting an OpenAI-style tools array
// intends to execute those calls itself and post the results back, so the
// agentic loop must neither execute them nor answer them — it must let the
// tool_calls response reach the client untouched.
func (r *Registry) Owns(toolName string) bool {
	_, ok := r.FindToolServer(toolName)
	return ok
}

// Acquire marks the registry as in use by one in-flight request and returns a
// release function. Close defers the actual teardown until every holder has
// released, so a config reload cannot terminate a stdio subprocess out from
// under a tool call that is still running.
//
// The returned function is idempotent. Acquiring an already-closed registry
// returns a no-op release rather than resurrecting it.
func (r *Registry) Acquire() func() {
	r.lifecycleMu.Lock()
	r.ensureLifecycleLocked()
	if r.closed {
		r.lifecycleMu.Unlock()
		return func() {}
	}
	r.active++
	r.lifecycleMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			r.lifecycleMu.Lock()
			r.active--
			if r.active == 0 {
				r.lifecycle.Broadcast()
			}
			r.lifecycleMu.Unlock()
		})
	}
}

// ensureLifecycleLocked lazily builds the condition variable so a Registry
// created by NewRegistry or as a zero value both work. Caller holds lifecycleMu.
func (r *Registry) ensureLifecycleLocked() {
	if r.lifecycle == nil {
		r.lifecycle = sync.NewCond(&r.lifecycleMu)
	}
}

// closeWhenDrained blocks until the last holder releases, then tears down.
func (r *Registry) closeWhenDrained() {
	r.lifecycleMu.Lock()
	r.ensureLifecycleLocked()
	for r.active > 0 {
		r.lifecycle.Wait()
	}
	r.lifecycleMu.Unlock()

	if err := r.closeClients(); err != nil {
		slog.Error("mcp: failed to close retired registry", "error", err)
	}
}

// Close shuts down all registered MCP server clients. For stdio servers this
// terminates the subprocess; for HTTP servers it is a no-op. Errors from
// individual clients are joined and returned together.
// Close is idempotent. When requests still hold the registry it returns
// immediately and teardown completes in the background once they release.
func (r *Registry) Close() error {
	r.lifecycleMu.Lock()
	r.ensureLifecycleLocked()
	if r.closed {
		r.lifecycleMu.Unlock()
		return nil
	}
	r.closed = true
	if r.active > 0 {
		r.lifecycleMu.Unlock()
		go r.closeWhenDrained()
		return nil
	}
	r.lifecycleMu.Unlock()

	return r.closeClients()
}

// closeClients tears down every transport concurrently.
//
// Serial teardown does not fit the shutdown budget: one wedged stdio server can
// spend seconds in the transport's graceful/SIGTERM/SIGKILL ladder, and
// Gateway.Close allows only 5s in total. Several of them serially would blow an
// orchestrator's grace period, which then SIGKILLs the process and re-orphans
// the very subprocesses the process-group sweep exists to reap.
func (r *Registry) closeClients() error {
	// Stop the transport watchers before detaching anything: they must not
	// outlive the clients they report on, and a probe already in flight is
	// cancelled rather than run to its timeout.
	if r.stopFunc != nil {
		r.stopFunc()
	}

	r.mu.Lock()
	clients := make([]mcpClient, 0, len(r.servers))
	for _, entry := range r.servers {
		if entry.client != nil {
			clients = append(clients, entry.client)
			// Detach so a second call cannot close the same transport again.
			// Close is guarded by the closed flag, but this function is reachable
			// from both Close and closeWhenDrained; leaving the references in
			// place made double-close depend on that guard alone.
			entry.client = nil
		}
		entry.ready = false
	}
	names := make([]string, 0, len(r.servers))
	for name := range r.servers {
		names = append(names, name)
	}
	r.mu.Unlock()

	// Drop the series rather than zero it, and only while this registry still
	// owns the label. A reload has already built the replacement by the time a
	// retired registry drains, and writing 0 here would take the live server
	// down on /metrics with nothing left to write 1 again.
	for _, name := range names {
		r.releaseServerUp(name)
	}

	errs := make([]error, len(clients))
	var wg sync.WaitGroup
	for i, c := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = c.Close()
		}()
	}
	wg.Wait()

	return errors.Join(errs...)
}

// AllTools returns the combined list of tools from all ready servers.
// Tool names are deduplicated using the first-registered-wins policy —
// when two servers expose the same tool name, the definition from the
// earlier-registered server is returned. Iteration order is deterministic
// (registration order) so callers always see consistent results.
func (r *Registry) AllTools() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	seen := make(map[string]bool, len(r.toolMap))
	tools := make([]Tool, 0, len(r.toolMap))
	for _, name := range r.regOrder {
		entry, ok := r.servers[name]
		if !ok || !entry.ready {
			continue
		}
		for _, t := range entry.tools {
			if !seen[t.Name] {
				seen[t.Name] = true
				tools = append(tools, t)
			}
		}
	}
	return tools
}

// ServerNames returns the names of all registered servers in registration order.
func (r *Registry) ServerNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, len(r.regOrder))
	copy(names, r.regOrder)
	return names
}

// IsReady returns true if the named server has completed initialization.
func (r *Registry) IsReady(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.servers[name]
	return ok && entry.ready
}

// HasServers reports whether any servers are registered.
func (r *Registry) HasServers() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.servers) > 0
}

// serverNameForTool returns the server name responsible for the given tool.
// Used for Prometheus metric labels. Returns "" if the tool is not found.
func (r *Registry) serverNameForTool(toolName string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.toolMap[toolName]
}

// timeoutForServer returns the configured per-call timeout for the named server.
// Falls back to 30 s when the server is not found or TimeoutSeconds is unset.
func (r *Registry) timeoutForServer(name string) time.Duration {
	r.mu.RLock()
	entry, ok := r.servers[name]
	r.mu.RUnlock()
	if ok && entry.config.TimeoutSeconds > 0 {
		return time.Duration(entry.config.TimeoutSeconds) * time.Second
	}
	return 30 * time.Second
}
