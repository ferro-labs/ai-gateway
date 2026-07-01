package aigateway

import (
	"context"
	"log/slog"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/mcp"
	pubmcp "github.com/ferro-labs/ai-gateway/mcp"
)

// MCP (Model Context Protocol) wiring for the Gateway: registry/executor
// construction, the audit-fn adapter, and the init-done accessor.

// buildMCPAuditFn converts the public ToolCallAuditFn into the internal
// mcp.AuditFn expected by the Executor.  Returns nil when fn is nil so the
// Executor skips audit logging entirely.
func buildMCPAuditFn(fn pubmcp.ToolCallAuditFn) mcp.AuditFn {
	if fn == nil {
		return nil
	}
	return func(ctx context.Context, serverName, toolName, status string, latencyMs int, errMsg string) {
		fn(ctx, pubmcp.ToolCallAuditEntry{
			ServerName:   serverName,
			ToolName:     toolName,
			Status:       status,
			LatencyMs:    latencyMs,
			ErrorMessage: errMsg,
		})
	}
}

// MCPInitDone returns a channel that is closed once all MCP servers have
// completed their initialization handshake.  The channel is pre-closed when
// no MCP servers are configured.
func (g *Gateway) MCPInitDone() <-chan struct{} {
	g.mu.RLock()
	done := g.mcpInitDone
	g.mu.RUnlock()
	if done == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return done
}

// mcpInitTimeout bounds the background MCP initialization handshake plus tool
// discovery. The timeout context is parented on the gateway shutdown context
// so Close() cancels a slow handshake instead of letting it linger for the
// full duration.
const mcpInitTimeout = 60 * time.Second

// wireMCPLocked (re)builds the MCP registry and executor from cfg and starts
// the background initialization goroutine. When cfg declares no MCP servers the
// MCP fields are cleared to nil. failLogMsg is logged for each server whose
// initialization handshake fails.
//
// It is safe to call from New (before the gateway is published, so no lock is
// held) and from ReloadConfig (with g.mu held by the caller); the field writes
// target the same gateway and, once published, are serialized by g.mu.
func (g *Gateway) wireMCPLocked(cfg Config, failLogMsg string) {
	if len(cfg.MCPServers) == 0 {
		g.mcpRegistry = nil
		g.mcpExecutor = nil
		g.mcpInitDone = nil
		return
	}

	reg := mcp.NewRegistry()
	for _, mcpCfg := range cfg.MCPServers {
		reg.RegisterConfig(mcpCfg)
	}

	maxDepth := minPositiveMaxCallDepth(cfg.MCPServers)

	g.mcpRegistry = reg
	g.mcpExecutor = mcp.NewExecutor(reg, maxDepth, buildMCPAuditFn(cfg.MCPToolCallAuditFn))

	// Handshake and tool discovery run in the background so the caller returns
	// immediately. mcpInitDone is closed once initialization completes so
	// callers can wait without polling via MCPInitDone().
	done := make(chan struct{})
	g.mcpInitDone = done
	go func() {
		defer close(done)
		// Parent the init timeout on the gateway shutdown context so a slow
		// MCP handshake is cancelled by Close() instead of lingering up to
		// the full timeout after shutdown.
		ctx, cancel := context.WithTimeout(g.shutdownCtx, mcpInitTimeout)
		defer cancel()
		reg.InitializeAll(ctx, func(name string, initErr error) {
			slog.Error(failLogMsg,
				"server", name,
				"error", initErr,
			)
		})
	}()
}

// minPositiveMaxCallDepth returns the smallest positive MaxCallDepth across the
// given MCP servers, or 0 when none specify a positive depth. A returned 0 lets
// mcp.NewExecutor apply its default (5).
func minPositiveMaxCallDepth(servers []pubmcp.ServerConfig) int {
	maxDepth := 0
	for _, mcpCfg := range servers {
		if mcpCfg.MaxCallDepth > 0 && (maxDepth == 0 || mcpCfg.MaxCallDepth < maxDepth) {
			maxDepth = mcpCfg.MaxCallDepth
		}
	}
	return maxDepth
}
