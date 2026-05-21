package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/ferro-labs/ai-gateway/internal/version"
)

// stdioClient wraps a mark3labs MCP client for the stdio transport.
// It launches a subprocess and communicates via stdin/stdout pipes,
// converting between mark3labs protocol types and ferro-labs internal types.
type stdioClient struct {
	inner mcpclient.MCPClient
}

// newStdioClient creates a stdio MCP client by launching command with args.
// Additional env pairs from envOverrides (KEY → VALUE) are merged on top of the
// current process environment. Returns an errClient instead of an error so that
// the Registry API (which has no error return) can defer the failure to
// InitializeAll, where it will be logged by the normal error path.
func newStdioClient(command string, args []string, envOverrides map[string]string) mcpClient {
	merged := os.Environ()
	for k, v := range envOverrides {
		merged = append(merged, k+"="+v)
	}

	c, err := mcpclient.NewStdioMCPClient(command, merged, args...)
	if err != nil {
		return &errClient{err: fmt.Errorf("mcp stdio: start %q: %w", command, err)}
	}
	return &stdioClient{inner: c}
}

// Initialize performs the MCP initialization handshake over stdio.
// mark3labs automatically sends the notifications/initialized notification.
func (c *stdioClient) Initialize(ctx context.Context) (*ServerInfo, error) {
	req := mcpgo.InitializeRequest{}
	req.Params.ProtocolVersion = mcpgo.LATEST_PROTOCOL_VERSION
	req.Params.ClientInfo = mcpgo.Implementation{
		Name:    "ferro-ai-gateway",
		Version: version.Short(),
	}

	result, err := c.inner.Initialize(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("mcp stdio initialize: %w", err)
	}

	return &ServerInfo{
		Name:    result.ServerInfo.Name,
		Version: result.ServerInfo.Version,
	}, nil
}

// ListTools fetches the tool list over stdio and converts it to ferro-labs types
// via a JSON round-trip. The mark3labs Tool type marshals to the same JSON
// structure as the ferro-labs Tool type (name, description, inputSchema).
func (c *stdioClient) ListTools(ctx context.Context) ([]Tool, error) {
	result, err := c.inner.ListTools(ctx, mcpgo.ListToolsRequest{})
	if err != nil {
		return nil, fmt.Errorf("mcp stdio tools/list: %w", err)
	}

	toolsJSON, err := json.Marshal(result.Tools)
	if err != nil {
		return nil, fmt.Errorf("mcp stdio tools/list marshal: %w", err)
	}
	var tools []Tool
	if err := json.Unmarshal(toolsJSON, &tools); err != nil {
		return nil, fmt.Errorf("mcp stdio tools/list unmarshal: %w", err)
	}
	return tools, nil
}

// CallTool invokes a named tool over stdio. Arguments are unmarshaled from
// RawMessage into any for mark3labs, and the result is converted back to
// ferro-labs types via a JSON round-trip.
func (c *stdioClient) CallTool(ctx context.Context, name string, arguments json.RawMessage) (*ToolCallResult, error) {
	req := mcpgo.CallToolRequest{}
	req.Params.Name = name

	if len(arguments) > 0 {
		var args any
		if err := json.Unmarshal(arguments, &args); err != nil {
			return nil, fmt.Errorf("mcp stdio tools/call %s: unmarshal args: %w", name, err)
		}
		req.Params.Arguments = args
	}

	result, err := c.inner.CallTool(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("mcp stdio tools/call %s: %w", name, err)
	}

	// Convert via JSON: mark3labs content blocks share the same JSON structure
	// as ferro-labs ContentBlock (type, text, data, mimeType, resource fields).
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("mcp stdio tools/call %s: marshal result: %w", name, err)
	}
	var toolResult ToolCallResult
	if err := json.Unmarshal(resultJSON, &toolResult); err != nil {
		return nil, fmt.Errorf("mcp stdio tools/call %s: unmarshal result: %w", name, err)
	}
	return &toolResult, nil
}

// Close terminates the stdio subprocess.
func (c *stdioClient) Close() error { return c.inner.Close() }

// ─── errClient ───────────────────────────────────────────────────────────────

// errClient is returned by newStdioClient when the subprocess cannot be started.
// Every method returns the construction-time error so that it surfaces in the
// normal InitializeAll error log rather than being silently swallowed.
type errClient struct {
	err error
}

func (e *errClient) Initialize(_ context.Context) (*ServerInfo, error) {
	return nil, e.err
}

func (e *errClient) ListTools(_ context.Context) ([]Tool, error) {
	return nil, e.err
}

func (e *errClient) CallTool(_ context.Context, _ string, _ json.RawMessage) (*ToolCallResult, error) {
	return nil, e.err
}

func (e *errClient) Close() error { return nil }
