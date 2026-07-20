package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/trace"
	"sync"
	"time"
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
	return &Registry{
		servers:     make(map[string]*serverEntry),
		toolMap:     make(map[string]string),
		serverIndex: make(map[string]int),
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
}

// InitializeAll performs the MCP handshake and tool discovery for every
// registered server that is not yet ready. It is idempotent and safe to call
// concurrently: each server is initialized at most once at a time even when
// multiple goroutines call InitializeAll simultaneously. Errors are reported
// via logErr (never returned) so the caller can log them without blocking.
func (r *Registry) InitializeAll(ctx context.Context, logErr func(name string, err error)) {
	ctx, task := trace.NewTask(ctx, "mcp.initialize_all")
	defer task.End()

	r.mu.RLock()
	names := make([]string, len(r.regOrder))
	copy(names, r.regOrder)
	r.mu.RUnlock()

	var wg sync.WaitGroup
	for _, name := range names {
		// Fast-path read: skip servers that are already done or in progress.
		r.mu.RLock()
		entry, ok := r.servers[name]
		skip := !ok || entry.ready || entry.initializing
		r.mu.RUnlock()
		if skip {
			continue
		}

		// Slow path: re-check under write lock before setting initializing flag.
		// This prevents two concurrent InitializeAll callers from both spawning
		// initServer goroutines for the same server.
		r.mu.Lock()
		entry, ok = r.servers[name]
		if !ok || entry.ready || entry.initializing {
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
		return fmt.Errorf("mcp: server %q not registered", name)
	}
	if client == nil {
		// The registry was closed while this initialisation was queued. Stop
		// rather than resurrecting a retired server.
		r.mu.Lock()
		entry.initializing = false
		r.mu.Unlock()
		return fmt.Errorf("mcp: server %q closed before initialization", name)
	}

	var err error
	trace.WithRegion(ctx, "mcp.init_server.initialize", func() {
		_, err = client.Initialize(ctx)
	})
	if err != nil {
		r.mu.Lock()
		entry.initErr = err
		entry.initializing = false
		r.mu.Unlock()
		return fmt.Errorf("mcp init %s: %w", name, err)
	}

	var tools []Tool
	trace.WithRegion(ctx, "mcp.init_server.list_tools", func() {
		tools, err = client.ListTools(ctx)
	})
	if err != nil {
		r.mu.Lock()
		entry.initErr = err
		entry.initializing = false
		r.mu.Unlock()
		return fmt.Errorf("mcp list tools %s: %w", name, err)
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

	return nil
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
	r.mu.Unlock()

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
