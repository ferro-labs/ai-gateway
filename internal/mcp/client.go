package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Client communicates with a single MCP server over Streamable HTTP transport.
// All exported methods are safe for concurrent use.
type Client struct {
	endpoint   string
	headers    map[string]string
	httpClient *http.Client

	// sessionMu protects sessionID. Written once during Initialize(), read on
	// every subsequent call. RWMutex ensures concurrent CallTool invocations
	// only contend for a read lock after the session is established.
	sessionMu sync.RWMutex
	sessionID string

	// nextID is incremented atomically to produce unique JSON-RPC request IDs.
	nextID atomic.Int64
}

// NewClient creates an MCP client for the given Streamable HTTP endpoint.
// timeout is the per-request HTTP timeout; 0 defaults to 30 seconds.
func NewClient(endpoint string, headers map[string]string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		endpoint: endpoint,
		headers:  headers,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// Initialize performs the MCP initialization handshake (initialize +
// notifications/initialized) and stores the Mcp-Session-Id for subsequent
// requests. Safe to call again — it will re-initialize the session.
func (c *Client) Initialize(ctx context.Context) (*ServerInfo, error) {
	params := map[string]interface{}{
		"protocolVersion": "2025-11-25",
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]string{
			"name":    "ferro-ai-gateway",
			"version": "0.8.0",
		},
	}

	resp, err := c.call(ctx, "initialize", params)
	if err != nil {
		return nil, fmt.Errorf("mcp initialize: %w", err)
	}

	var info ServerInfo
	if err := json.Unmarshal(resp.Result, &info); err != nil {
		return nil, fmt.Errorf("mcp initialize unmarshal: %w", err)
	}

	// Send the initialized notification. The MCP spec says no response is
	// expected; errors here are non-fatal (server may still be usable).
	_ = c.notify(ctx, "notifications/initialized", nil)

	return &info, nil
}

// ListTools retrieves the full list of tools from the MCP server.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	resp, err := c.call(ctx, "tools/list", nil)
	if err != nil {
		return nil, fmt.Errorf("mcp tools/list: %w", err)
	}

	var result struct {
		Tools []Tool `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("mcp tools/list unmarshal: %w", err)
	}
	return result.Tools, nil
}

// CallTool invokes a named tool on the MCP server with the given JSON-encoded
// arguments. Safe for concurrent use from multiple goroutines.
func (c *Client) CallTool(ctx context.Context, name string, arguments json.RawMessage) (*ToolCallResult, error) {
	params := map[string]interface{}{
		"name":      name,
		"arguments": arguments,
	}

	resp, err := c.call(ctx, "tools/call", params)
	if err != nil {
		return nil, fmt.Errorf("mcp tools/call %s: %w", name, err)
	}

	var result ToolCallResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("mcp tools/call %s unmarshal: %w", name, err)
	}
	return &result, nil
}

// call sends a JSON-RPC 2.0 request and returns the decoded response.
// It sets all required headers including the session ID once established.
func (c *Client) call(ctx context.Context, method string, params interface{}) (*JSONRPCResponse, error) {
	id := c.nextID.Add(1)

	var rawParams json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("mcp marshal params: %w", err)
		}
		rawParams = b
	}

	rpcReq := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  rawParams,
	}
	body, err := json.Marshal(rpcReq)
	if err != nil {
		return nil, fmt.Errorf("mcp marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("mcp new http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	if sid := c.getSessionID(); sid != "" {
		httpReq.Header.Set("Mcp-Session-Id", sid)
	}
	for k, v := range c.headers {
		httpReq.Header.Set(k, v)
	}

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("mcp http do: %w", err)
	}
	defer httpResp.Body.Close()

	// Persist the session ID returned by the server (set on initialize).
	c.setSessionID(httpResp.Header.Get("Mcp-Session-Id"))

	if httpResp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(httpResp.Body, 4096))
		return nil, fmt.Errorf("mcp server %s returned HTTP %d: %s", method, httpResp.StatusCode, errBody)
	}

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("mcp read body: %w", err)
	}

	var rpcResp JSONRPCResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return nil, fmt.Errorf("mcp response unmarshal: %w", err)
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("mcp rpc error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	return &rpcResp, nil
}

// notify sends a JSON-RPC 2.0 notification (no ID, no response expected).
func (c *Client) notify(ctx context.Context, method string, params interface{}) error {
	var rawParams json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		rawParams = b
	}

	notification := struct {
		JSONRPC string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params,omitempty"`
	}{
		JSONRPC: "2.0",
		Method:  method,
		Params:  rawParams,
	}
	body, err := json.Marshal(notification)
	if err != nil {
		return err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if sid := c.getSessionID(); sid != "" {
		httpReq.Header.Set("Mcp-Session-Id", sid)
	}
	for k, v := range c.headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// getSessionID reads the current session ID under a read lock.
func (c *Client) getSessionID() string {
	c.sessionMu.RLock()
	defer c.sessionMu.RUnlock()
	return c.sessionID
}

// setSessionID writes the session ID under a write lock. Empty strings are ignored.
func (c *Client) setSessionID(sid string) {
	if sid == "" {
		return
	}
	c.sessionMu.Lock()
	c.sessionID = sid
	c.sessionMu.Unlock()
}
