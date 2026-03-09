// Package mcp exposes the public configuration types for Ferro Labs AI Gateway's
// MCP (Model Context Protocol) integration.
//
// The types in this package are intentionally shallow — they carry only what an
// external consumer needs to wire an MCP server into gateway.Config. All
// protocol-level types (JSON-RPC envelopes, Tool definitions, etc.) remain
// unexported in internal/mcp.
package mcp

// ServerConfig defines how the gateway connects to one external MCP server.
// It lives in gateway.Config.MCPServers and is consumed by the internal Registry.
type ServerConfig struct {
	// Name is a unique human-readable identifier for this MCP server.
	Name string `json:"name" yaml:"name"`
	// URL is the Streamable HTTP endpoint (e.g. "https://mcp.example.com/mcp").
	URL string `json:"url" yaml:"url"`
	// Headers are additional HTTP headers sent with every MCP request
	// (e.g. authorization tokens). Values may reference environment variables
	// via shell-style ${VAR} substitution performed by the caller.
	Headers map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
	// AllowedTools restricts which tools from this server are exposed to the LLM.
	// An empty slice means all discovered tools are allowed.
	AllowedTools []string `json:"allowed_tools,omitempty" yaml:"allowed_tools,omitempty"`
	// MaxCallDepth limits the agentic tool-calling depth for this server.
	// The minimum positive value across all configured servers is used;
	// servers with MaxCallDepth ≤ 0 are excluded from the minimum.
	// Defaults to 5 when all servers leave MaxCallDepth unset or zero.
	MaxCallDepth int `json:"max_call_depth,omitempty" yaml:"max_call_depth,omitempty"`
	// TimeoutSeconds is the per-request timeout for calls to this server.
	// Defaults to 30 when unset or zero.
	TimeoutSeconds int `json:"timeout_seconds,omitempty" yaml:"timeout_seconds,omitempty"`
}
