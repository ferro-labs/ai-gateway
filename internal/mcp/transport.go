package mcp

import (
	"context"
	"encoding/json"
)

// mcpClient is the internal interface all MCP transport adapters must satisfy.
// Both the hand-rolled HTTP client (*Client) and the stdio adapter (*stdioClient)
// implement this interface so the Registry is transport-agnostic.
type mcpClient interface {
	// Initialize performs the MCP initialization handshake with the server.
	Initialize(ctx context.Context) (*ServerInfo, error)
	// ListTools retrieves the full list of tools advertised by the server.
	ListTools(ctx context.Context) ([]Tool, error)
	// CallTool invokes a named tool with JSON-encoded arguments.
	CallTool(ctx context.Context, name string, arguments json.RawMessage) (*ToolCallResult, error)
	// Close releases any resources held by the transport (e.g. subprocess, connection).
	Close() error
}
