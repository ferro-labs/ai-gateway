package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/admin/repository"
	"github.com/go-chi/chi/v5"
)

// mcpHealthBody is the part of the /admin/health response these tests read.
type mcpHealthBody struct {
	Status     string            `json:"status"`
	MCPServers []MCPServerHealth `json:"mcp_servers"`
}

// getAdminHealth serves one authenticated GET /admin/health against a Handlers
// carrying the given MCP status source.
func getAdminHealth(t *testing.T, status func() []MCPServerHealth) mcpHealthBody {
	t.Helper()

	store := repository.NewKeyStore()
	h := &Handlers{Keys: store, Providers: registryWith(adminHealthProvider{}), MCPStatus: status}
	r := chi.NewRouter()
	r.Use(AuthMiddleware(store, ""))
	r.Mount("/admin", h.Routes())
	key := createAdminKey(t, h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodGet, "/admin/health", "", key))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /admin/health = %d, want 200: %s", w.Code, w.Body.String())
	}
	var body mcpHealthBody
	decodeJSON(t, w.Body, &body)
	return body
}

// The failure reason is served by no other endpoint: /readyz is unauthenticated
// and has to withhold it, which left an operator reading server logs to learn
// why a tool server was down. This route is bearer-authenticated, so it carries
// it.
func TestHealthCheck_ServesTheMCPFailureReason(t *testing.T) {
	const reason = `mcp init search: Post "https://mcp.example.com/mcp": connection refused`

	body := getAdminHealth(t, func() []MCPServerHealth {
		return []MCPServerHealth{{Name: "search", Ready: false, Required: false, LastError: reason}}
	})

	if len(body.MCPServers) != 1 {
		t.Fatalf("mcp_servers = %+v, want one entry", body.MCPServers)
	}
	got := body.MCPServers[0]
	if got.Name != "search" || got.Ready {
		t.Errorf("mcp_servers[0] = %+v, want the unready server", got)
	}
	if got.LastError != reason {
		t.Errorf("last_error = %q, want %q", got.LastError, reason)
	}
	// An optional server that is down costs its own tools and nothing else, so
	// the instance is not degraded by it — /readyz keeps serving too.
	if body.Status != healthStatusHealthy {
		t.Errorf("status = %q, want %q: an optional MCP server must not degrade the gateway", body.Status, healthStatusHealthy)
	}
}

// A required server that is down stops every request through the gateway, which
// is where /readyz gates. The authenticated view must not report that instance
// healthy while the unauthenticated one answers 503.
func TestHealthCheck_RequiredMCPServerDownDegradesTheGateway(t *testing.T) {
	body := getAdminHealth(t, func() []MCPServerHealth {
		return []MCPServerHealth{
			{Name: "filesystem", Ready: true, Required: false},
			{Name: "search", Ready: false, Required: true, LastError: "handshake timed out"},
		}
	})

	if body.Status != healthStatusDegraded {
		t.Errorf("status = %q, want %q", body.Status, healthStatusDegraded)
	}
	if len(body.MCPServers) != 2 {
		t.Fatalf("mcp_servers = %+v, want both servers reported", body.MCPServers)
	}
	// A ready server carries no reason, and omitempty keeps the key out of the
	// response rather than publishing an empty one.
	if body.MCPServers[0].LastError != "" {
		t.Errorf("a ready server reported last_error = %q", body.MCPServers[0].LastError)
	}
}

// A gateway with no MCP servers configured must not grow an empty list: that
// reads as "every MCP server is gone" on a deployment that never had one.
func TestHealthCheck_OmitsMCPServersWhenNoneAreConfigured(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status func() []MCPServerHealth
	}{
		{name: "no source wired", status: nil},
		{name: "source reports none", status: func() []MCPServerHealth { return nil }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := repository.NewKeyStore()
			h := &Handlers{Keys: store, Providers: registryWith(adminHealthProvider{}), MCPStatus: tt.status}
			r := chi.NewRouter()
			r.Use(AuthMiddleware(store, ""))
			r.Mount("/admin", h.Routes())
			key := createAdminKey(t, h)

			w := httptest.NewRecorder()
			r.ServeHTTP(w, authedRequest(http.MethodGet, "/admin/health", "", key))

			var raw map[string]any
			decodeJSON(t, w.Body, &raw)
			if _, ok := raw["mcp_servers"]; ok {
				t.Errorf("mcp_servers = %v, want the key absent", raw["mcp_servers"])
			}
		})
	}
}
