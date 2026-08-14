package api

// Wire shapes for the gateway's read endpoints. Every json tag here is the
// gateway's own struct tag, verbatim — the gateway is the schema, this file is
// only a mirror of it, so nothing is renamed for taste.

// ProviderHealth is one row of the unauthenticated /health provider list.
type ProviderHealth struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Circuit string `json:"circuit"` // closed|open|half_open
	Models  int    `json:"models"`
}

// HealthReport is GET /health: 200 when serving, 503 when no provider is up.
type HealthReport struct {
	Status    string           `json:"status"` // ok|no_providers
	Providers []ProviderHealth `json:"providers"`
}

// ReadyTarget is a configured routing target and whether a request can reach it.
type ReadyTarget struct {
	Name     string `json:"name"`
	Routable bool   `json:"routable"`
}

// ReadyProvider is the circuit-only provider view served by /readyz.
type ReadyProvider struct {
	Name    string `json:"name"`
	Circuit string `json:"circuit"`
}

// MCPServer is one MCP tool server. LastError is served by /admin/health only:
// /readyz is unauthenticated and the reason can quote a URL or a token.
type MCPServer struct {
	Name      string `json:"name"`
	Ready     bool   `json:"ready"`
	Required  bool   `json:"required"`
	LastError string `json:"last_error,omitempty"`
}

// ReadyReport is GET /readyz: 200 ready, or 503 with Reason set.
type ReadyReport struct {
	Status     string          `json:"status"` // ready|not_ready
	Reason     string          `json:"reason,omitempty"`
	Providers  []ReadyProvider `json:"providers,omitempty"`
	Targets    []ReadyTarget   `json:"targets,omitempty"`
	MCPServers []MCPServer     `json:"mcp_servers,omitempty"`
}

// AdminProviderHealth is one row of the authenticated provider list, which
// carries a message a failing provider explains itself with.
type AdminProviderHealth struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // healthy|degraded|disabled|unavailable|available
	Models  int    `json:"models"`
	Message string `json:"message,omitempty"`
}

// AdminComponent is a subsystem (API, key store, log store, …) and its status.
type AdminComponent struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// AdminHealth is GET /admin/health, which always answers 200 when the caller
// is authenticated — so it doubles as ferro's auth and scope probe.
type AdminHealth struct {
	Status     string                `json:"status"`
	Providers  []AdminProviderHealth `json:"providers"`
	Components []AdminComponent      `json:"components"`
	MCPServers []MCPServer           `json:"mcp_servers,omitempty"`
	Scopes     []string              `json:"scopes,omitempty"`
}

// Model is one entry of the OpenAI-shaped GET /v1/models listing.
type Model struct {
	ID              string   `json:"id"`
	Object          string   `json:"object"`
	Created         int64    `json:"created"`
	OwnedBy         string   `json:"owned_by"`
	Mode            string   `json:"mode,omitempty"`
	ContextWindow   int      `json:"context_window,omitempty"`
	MaxOutputTokens int      `json:"max_output_tokens,omitempty"`
	Capabilities    []string `json:"capabilities,omitempty"`
	Status          string   `json:"status,omitempty"`
	Deprecated      bool     `json:"deprecated,omitempty"`
}
