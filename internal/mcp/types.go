// Package mcp implements the Model Context Protocol (MCP) 2025-11-25
// Streamable HTTP transport for the Ferro Labs AI Gateway.
//
// It provides a thread-safe client, a concurrent-safe server registry,
// and an agentic tool-call loop executor that integrates with gateway.Route.
package mcp

import (
	"encoding/json"

	mcpconfig "github.com/ferro-labs/ai-gateway/mcp"
)

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
// Type is one of "text", "image", or "resource" (MCP 2025-11-25 §4.5).
//
// Phase 1 only extracts the text payload for conversation messages;
// non-text fields are decoded and preserved but not converted to prose.
type ContentBlock struct {
	Type string `json:"type"`
	// Text carries the content for type="text" blocks.
	Text string `json:"text,omitempty"`
	// Data and MimeType are populated for type="image" blocks.
	// Data is a base64-encoded payload; MimeType is e.g. "image/png".
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	// Resource holds the embedded resource object for type="resource" blocks.
	// Stored as raw JSON for forward compatibility with future MCP spec revisions.
	Resource json.RawMessage `json:"resource,omitempty"`
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

// ServerConfig is a type alias for [mcpconfig.ServerConfig] so that all code
// inside internal/mcp can refer to it by the short name ServerConfig while
// external consumers use the public github.com/ferro-labs/ai-gateway/mcp package.
type ServerConfig = mcpconfig.ServerConfig
