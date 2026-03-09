// Package mcp implements the Model Context Protocol (MCP) 2025-11-25
// Streamable HTTP transport for the Ferro Labs AI Gateway.
//
// It provides a thread-safe client, a concurrent-safe server registry,
// and an agentic tool-call loop executor that integrates with gateway.Route.
package mcp

import "encoding/json"

// MCP protocol method names used in JSON-RPC calls.
const (
	mcpMethodInitialize = "initialize"
	mcpMethodToolsList  = "tools/list"
	mcpMethodToolsCall  = "tools/call"
)

// ─── JSON-RPC 2.0 ────────────────────────────────────────────────────────────

// JSONRPCRequest is a JSON-RPC 2.0 request envelope.
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// JSONRPCResponse is a JSON-RPC 2.0 response envelope.
type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

// JSONRPCError is the error object nested inside a failed JSON-RPC 2.0 response.
type JSONRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// ─── MCP Protocol Types ───────────────────────────────────────────────────────

// Tool represents an MCP tool definition as returned by tools/list.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ToolCallResult holds the result of a single tools/call invocation.
type ToolCallResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// ContentBlock is a single piece of content returned by a tool call.
// The Type field is one of "text", "image", or "resource".
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ServerInfo describes the MCP server returned during the initialize handshake.
type ServerInfo struct {
	Name         string       `json:"name"`
	Version      string       `json:"version"`
	Capabilities Capabilities `json:"capabilities"`
}

// Capabilities advertised by an MCP server during initialization.
type Capabilities struct {
	Tools     *ToolsCapability     `json:"tools,omitempty"`
	Resources *ResourcesCapability `json:"resources,omitempty"`
	Prompts   *PromptsCapability   `json:"prompts,omitempty"`
}

// ToolsCapability advertises tool-related server capabilities.
type ToolsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// ResourcesCapability advertises resource-related server capabilities.
type ResourcesCapability struct {
	Subscribe   bool `json:"subscribe,omitempty"`
	ListChanged bool `json:"listChanged,omitempty"`
}

// PromptsCapability advertises prompt-related server capabilities.
type PromptsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// ─── Gateway Config Type ──────────────────────────────────────────────────────

// ServerConfig defines how the gateway connects to one external MCP server.
// It lives in gateway.Config.MCPServers and is consumed by the Registry.
//
// In FerroCloud, encrypted headers are decrypted from headers_enc using
// FG_ENCRYPTION_KEY before being placed into the Headers map.
type ServerConfig struct {
	// Name is a unique human-readable identifier for this MCP server.
	Name string `json:"name" yaml:"name"`
	// URL is the Streamable HTTP endpoint (e.g. "https://mcp.example.com/mcp").
	URL string `json:"url" yaml:"url"`
	// Headers are additional HTTP headers sent with every MCP request
	// (e.g. authorisation tokens). Values may reference environment variables
	// via shell-style ${VAR} substitution performed by the caller.
	Headers map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
	// AllowedTools restricts which tools from this server are exposed to the LLM.
	// An empty slice means all discovered tools are allowed.
	AllowedTools []string `json:"allowed_tools,omitempty" yaml:"allowed_tools,omitempty"`
	// MaxCallDepth limits the agentic tool-calling depth for this server.
	// When multiple servers are registered the lowest positive value wins.
	// Defaults to 5 when unset or zero.
	MaxCallDepth int `json:"max_call_depth,omitempty" yaml:"max_call_depth,omitempty"`
	// TimeoutSeconds is the per-request timeout for calls to this server.
	// Defaults to 30 when unset or zero.
	TimeoutSeconds int `json:"timeout_seconds,omitempty" yaml:"timeout_seconds,omitempty"`
}
