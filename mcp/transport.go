package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"syscall"

	"github.com/mark3labs/mcp-go/client/transport"
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

// pinger is the optional confirmation probe a transport may offer. MCP makes
// ping mandatory for servers, so a transport that can issue one can distinguish
// a server that has genuinely gone from one that merely looks that way.
//
// Optional rather than part of mcpClient: only the stdio transport has a death
// signal to confirm, and widening the interface would oblige every adapter to
// carry a method nothing calls on it.
type pinger interface {
	Ping(ctx context.Context) error
}

// isTransportDead reports whether err is conclusive evidence that the server on
// the other end of the transport is gone, as opposed to one call going wrong.
//
// Two errors qualify. ErrTransportClosed means the transport's own reader saw
// stdout EOF and closed itself. EPIPE (equivalently io.ErrClosedPipe) means a
// write reached a pipe with no reader left — the case where a descendant still
// holds the pipes open, so the transport never noticed and reports the raw
// syscall error instead.
//
// Nothing else belongs here. A timeout, a refused connection, or a malformed
// frame can all resolve on their own, and withdrawing a server is terminal
// until the next configuration reload.
func isTransportDead(err error) bool {
	return errors.Is(err, transport.ErrTransportClosed) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, io.ErrClosedPipe)
}
