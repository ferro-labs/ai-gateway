// Package mcp exposes the public configuration types for Ferro Labs AI Gateway's
// MCP (Model Context Protocol) integration.
//
// The types in this package are intentionally shallow — they carry only what an
// external consumer needs to wire an MCP server into gateway.Config. All
// protocol-level types (JSON-RPC envelopes, Tool definitions, etc.) remain
// unexported in internal/mcp.
package mcp

import "context"

// ServerConfig defines how the gateway connects to one external MCP server.
// It lives in gateway.Config.MCPServers and is consumed by the internal Registry.
//
// Transport selection: when Command is non-empty the gateway uses stdio transport
// (the server is launched as a subprocess). Otherwise Streamable HTTP transport
// is used and URL must be provided.
type ServerConfig struct {
	// Name is a unique human-readable identifier for this MCP server.
	Name string `json:"name" yaml:"name"`

	// ── HTTP transport (URL must be set when Command is empty) ───────────────
	// URL is the Streamable HTTP endpoint (e.g. "https://mcp.example.com/mcp").
	URL string `json:"url,omitempty" yaml:"url,omitempty"`
	// Headers are additional HTTP headers sent with every MCP request
	// (e.g. authorization tokens). Values may reference environment variables
	// using ${VAR} — only the braced form is a reference, a bare $ is literal
	// data, and an undefined variable is an error rather than a blank secret.
	// References are resolved when the MCP client is constructed, not when the
	// config is loaded, so the Config itself never carries a materialised secret
	// into the config-history store or GET /admin/config.
	//
	// Ignored for stdio servers (those setting Command).
	Headers map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`

	// ── Stdio transport (Command must be set; URL/Headers are ignored) ───────
	// Command is the executable to launch as an MCP stdio server.
	// The process is started at gateway-init time and kept alive for the
	// lifetime of the gateway.
	Command string `json:"command,omitempty" yaml:"command,omitempty"`
	// Args are the command-line arguments passed to Command.
	Args []string `json:"args,omitempty" yaml:"args,omitempty"`
	// Env are the environment variables injected into the subprocess.
	//
	// The subprocess does NOT inherit the gateway's environment. It receives a
	// minimal base — PATH, HOME, LANG and TMPDIR, when set — plus exactly the
	// keys listed here, which override the base. This keeps gateway credentials
	// such as OPENAI_API_KEY and MASTER_KEY out of MCP server processes.
	//
	// A consequence worth knowing: servers that need other inherited variables
	// (HTTPS_PROXY, NODE_PATH, SSL_CERT_FILE, or SYSTEMROOT and APPDATA on
	// Windows) must have them listed here explicitly.
	//
	// Values may reference environment variables using ${VAR} — only the braced
	// form, same rules as Headers — resolved when the MCP client is constructed.
	// Since the gateway's own environment is not inherited, this is the only
	// channel by which a credential can reach an MCP subprocess.
	Env map[string]string `json:"env,omitempty" yaml:"env,omitempty"`

	// ── Common options ────────────────────────────────────────────────────────
	// AllowedTools restricts which tools from this server are exposed to the LLM.
	// An empty slice means all discovered tools are allowed.
	AllowedTools []string `json:"allowed_tools,omitempty" yaml:"allowed_tools,omitempty"`
	// MaxCallDepth limits the agentic tool-calling depth for this server.
	// The minimum positive value across all registered servers is used;
	// servers with MaxCallDepth ≤ 0 are excluded from the minimum.
	// Defaults to 5 when all servers leave MaxCallDepth unset or zero.
	MaxCallDepth int `json:"max_call_depth,omitempty" yaml:"max_call_depth,omitempty"`
	// TimeoutSeconds is the per-request timeout for individual tool calls to
	// this server. Applies to both HTTP and stdio transports. Defaults to 30
	// when unset or zero.
	TimeoutSeconds int `json:"timeout_seconds,omitempty" yaml:"timeout_seconds,omitempty"`
}

// ToolCallAuditEntry contains metadata captured after a single MCP tool
// invocation. It is passed to [ToolCallAuditFn] on every tool call.
type ToolCallAuditEntry struct {
	// ServerName is the name of the MCP server that owns the tool.
	ServerName string
	// ToolName is the name of the tool that was called.
	ToolName string
	// Status is "ok" on success, "error" on failure.
	Status string
	// LatencyMs is the wall-clock time of the CallTool RPC in milliseconds.
	LatencyMs int
	// ErrorMessage is non-empty when Status is "error".
	ErrorMessage string
}

// ToolCallAuditFn is an optional callback invoked after every MCP tool
// invocation (success or failure). Implementations must be non-blocking;
// delegate any I/O to a goroutine.
//
// The ctx is the same context used for the Route call — callers may embed
// per-request values (e.g. trace ID, API key ID) for retrieval inside the hook.
// Set MCPToolCallAuditFn on aigateway.Config before calling New.
type ToolCallAuditFn func(ctx context.Context, entry ToolCallAuditEntry)
