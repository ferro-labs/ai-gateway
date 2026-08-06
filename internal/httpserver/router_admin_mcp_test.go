package httpserver_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/internal/admin/model"
	"github.com/ferro-labs/ai-gateway/internal/admin/repository"
	"github.com/ferro-labs/ai-gateway/internal/httpserver"
	"github.com/ferro-labs/ai-gateway/mcp"
	"github.com/ferro-labs/ai-gateway/providers"
)

// deadMCPURL points at a port nothing listens on, so the initialize handshake
// fails immediately and the server stays unready with a reason recorded. That
// needs no MCP server to be running.
const deadMCPURL = "http://127.0.0.1:1/mcp"

// mcpServerBody is the MCP shape both endpoints report. last_error is declared
// for both so the test can assert /readyz leaves it empty rather than only that
// /admin/health fills it in.
type mcpServerBody struct {
	Name      string `json:"name"`
	Ready     bool   `json:"ready"`
	Required  bool   `json:"required"`
	LastError string `json:"last_error"`
}

// TestAdminHealth_ServesTheMCPFailureReasonReadyzWithholds pins the split
// through the production wiring: the reason a server is unready reaches the
// bearer-authenticated /admin/health and never the unauthenticated /readyz.
//
// Both halves matter. Without the first the reason is served by no endpoint at
// all and an operator has to read server logs; without the second a reason that
// can quote a server URL, an authorization header, or a subprocess command line
// is public.
func TestAdminHealth_ServesTheMCPFailureReasonReadyzWithholds(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy:   config.StrategyConfig{Mode: config.ModeSingle},
		Targets:    []config.Target{{VirtualKey: "stub"}},
		MCPServers: []mcp.ServerConfig{{Name: "tools", URL: deadMCPURL, TimeoutSeconds: 1}},
	})
	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}
	gw.RegisterProvider(stubProvider{})

	// The handshake runs in the background from gateway construction; waiting
	// is what makes the recorded reason deterministic.
	select {
	case <-gw.MCPInitDone():
	case <-time.After(30 * time.Second):
		t.Fatal("MCP initialization did not finish")
	}

	reg := providers.NewRegistry()
	reg.Register(stubProvider{})
	keyStore := repository.NewKeyStore()
	// The lowest scope the route accepts: diagnosing a tool server must not
	// require a credential that can also rewrite the config.
	key, err := keyStore.Create(t.Context(), "ops", []string{model.ScopeReadOnly}, nil)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	router := httpserver.NewRouter(reg, keyStore, nil, nil, gw, nil, nil, nil, nil, nil, "", nil)

	get := func(path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	adminW := get("/admin/health", key.Key)
	if adminW.Code != http.StatusOK {
		t.Fatalf("GET /admin/health = %d, want 200: %s", adminW.Code, adminW.Body.String())
	}
	var adminBody struct {
		MCPServers []mcpServerBody `json:"mcp_servers"`
	}
	if err := json.Unmarshal(adminW.Body.Bytes(), &adminBody); err != nil {
		t.Fatalf("decode /admin/health: %v", err)
	}
	if len(adminBody.MCPServers) != 1 {
		t.Fatalf("/admin/health mcp_servers = %+v, want one entry", adminBody.MCPServers)
	}
	server := adminBody.MCPServers[0]
	if server.Name != "tools" || server.Ready {
		t.Errorf("/admin/health mcp_servers[0] = %+v, want the unready server named", server)
	}
	if server.LastError == "" {
		t.Error("/admin/health carried no last_error: the reason is then served by no endpoint at all")
	}

	// Unauthenticated, as an orchestrator reads it.
	readyW := get("/readyz", "")
	if readyW.Code != http.StatusOK {
		t.Fatalf("GET /readyz = %d, want 200: an optional MCP server must not gate readiness: %s", readyW.Code, readyW.Body.String())
	}
	if strings.Contains(readyW.Body.String(), "last_error") {
		t.Errorf("/readyz body carried last_error: %s", readyW.Body.String())
	}
	var readyBody struct {
		MCPServers []mcpServerBody `json:"mcp_servers"`
	}
	if err := json.Unmarshal(readyW.Body.Bytes(), &readyBody); err != nil {
		t.Fatalf("decode /readyz: %v", err)
	}
	// The server is still reported there — the withholding is field-level, so
	// this test fails if /readyz stops reporting MCP state for another reason.
	if len(readyBody.MCPServers) != 1 || readyBody.MCPServers[0].Name != "tools" {
		t.Fatalf("/readyz mcp_servers = %+v, want the same server reported", readyBody.MCPServers)
	}
	if readyBody.MCPServers[0].LastError != "" {
		t.Errorf("/readyz reported last_error = %q, want it withheld", readyBody.MCPServers[0].LastError)
	}
}
