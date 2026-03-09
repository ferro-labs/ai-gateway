package mcp

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Registry manages registered MCP servers and the tools they expose.
// All methods are safe for concurrent use.
type Registry struct {
	mu      sync.RWMutex
	servers map[string]*serverEntry // server name => entry
	toolMap map[string]string       // tool name => server name (O(1) lookup)
}

// serverEntry holds the live state for one registered MCP server.
type serverEntry struct {
	config  ServerConfig
	client  *Client
	tools   []Tool
	ready   bool  // true once Initialize + ListTools have succeeded
	initErr error // last initialization error; nil when ready
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		servers: make(map[string]*serverEntry),
		toolMap: make(map[string]string),
	}
}

// RegisterConfig stores an MCP server configuration and creates its client
// without making any network calls. Call InitializeAll in a background
// goroutine after gateway.New() returns so the first LLM request is never
// blocked by MCP cold-start latency.
func (r *Registry) RegisterConfig(cfg ServerConfig) {
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	client := NewClient(cfg.URL, cfg.Headers, timeout)

	r.mu.Lock()
	r.servers[cfg.Name] = &serverEntry{
		config: cfg,
		client: client,
	}
	r.mu.Unlock()
}

// InitializeAll performs the MCP handshake and tool discovery for every
// registered server that is not yet ready. It is idempotent — already-ready
// servers are skipped. Designed to be called from a background goroutine;
// errors are reported via logErr (never returned) so the caller can log them.
func (r *Registry) InitializeAll(ctx context.Context, logErr func(name string, err error)) {
	r.mu.RLock()
	names := make([]string, 0, len(r.servers))
	for name := range r.servers {
		names = append(names, name)
	}
	r.mu.RUnlock()

	var wg sync.WaitGroup
	for _, name := range names {
		r.mu.RLock()
		entry, ok := r.servers[name]
		alreadyReady := ok && entry.ready
		r.mu.RUnlock()

		if !ok || alreadyReady {
			continue
		}

		wg.Add(1)
		go func(n string) {
			defer wg.Done()
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
	r.mu.RLock()
	entry, ok := r.servers[name]
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("mcp: server %q not registered", name)
	}

	_, err := entry.client.Initialize(ctx)
	if err != nil {
		r.mu.Lock()
		entry.initErr = err
		r.mu.Unlock()
		return fmt.Errorf("mcp init %s: %w", name, err)
	}

	tools, err := entry.client.ListTools(ctx)
	if err != nil {
		r.mu.Lock()
		entry.initErr = err
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
	entry.tools = tools
	entry.ready = true
	entry.initErr = nil
	for _, t := range tools {
		r.toolMap[t.Name] = name
	}
	r.mu.Unlock()

	return nil
}

// FindToolServer returns the Client responsible for the named tool.
// Returns (nil, false) when no ready server exposes the tool.
func (r *Registry) FindToolServer(toolName string) (*Client, bool) {
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

// AllTools returns the combined list of tools from all ready servers.
// Tool names are deduplicated: when the same tool name is advertised by
// multiple servers, only the first encountered definition is included.
func (r *Registry) AllTools() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	seen := make(map[string]bool, len(r.toolMap))
	var tools []Tool
	for _, entry := range r.servers {
		if !entry.ready {
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

// ServerNames returns the names of all registered servers.
func (r *Registry) ServerNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.servers))
	for name := range r.servers {
		names = append(names, name)
	}
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
