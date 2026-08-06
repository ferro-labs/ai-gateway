//go:build integration
// +build integration

package http_test

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/mcp"
)

// deadMCPURL points at a port nothing listens on, so the initialize handshake
// fails immediately and the server stays unready for the life of the test. That
// is the state both readiness cases below turn on, and it needs no MCP server.
const deadMCPURL = "http://127.0.0.1:1/mcp"

type readyzBody struct {
	Status     string `json:"status"`
	Reason     string `json:"reason"`
	MCPServers []struct {
		Name     string `json:"name"`
		Ready    bool   `json:"ready"`
		Required bool   `json:"required"`
	} `json:"mcp_servers"`
}

// getReadyz waits for MCP initialization to settle, then reads /readyz through
// the wired router. Waiting is what makes the answer deterministic: the
// handshake runs in the background from gateway construction.
func getReadyz(t *testing.T, env *testEnv) (int, readyzBody) {
	t.Helper()

	select {
	case <-env.Gateway.MCPInitDone():
	case <-time.After(30 * time.Second):
		t.Fatal("MCP initialization did not finish")
	}

	resp, err := http.DefaultClient.Do(newTestRequest(t, http.MethodGet, env.Server.URL+"/readyz", nil))
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /readyz: %v", err)
	}
	var body readyzBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode /readyz %s: %v", raw, err)
	}
	return resp.StatusCode, body
}

// An optional MCP server that is down is reported, not gated on: pulling an
// instance out of rotation over a tool server that most requests never touch
// makes the outage worse.
func TestReadyz_OptionalMCPServerDownIsReportedNotGated(t *testing.T) {
	env := newTestServer(t, withMCPServers(mcp.ServerConfig{
		Name: "optional-tools", URL: deadMCPURL, TimeoutSeconds: 1,
	}))

	status, body := getReadyz(t, env)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200: an optional MCP server must not gate readiness (%+v)", status, body)
	}
	if body.Status != "ready" {
		t.Errorf("status field = %q, want ready", body.Status)
	}
	if len(body.MCPServers) != 1 {
		t.Fatalf("mcp_servers = %+v, want one entry", body.MCPServers)
	}
	got := body.MCPServers[0]
	if got.Name != "optional-tools" || got.Ready || got.Required {
		t.Errorf("mcp_servers[0] = %+v, want {optional-tools false false}", got)
	}
}

// required: true is the opt-in lever that does gate readiness. The reason is a
// fixed string — /readyz is unauthenticated and the underlying error can quote
// a server URL or an authorization header.
func TestReadyz_RequiredMCPServerDownIsNotReady(t *testing.T) {
	env := newTestServer(t, withMCPServers(mcp.ServerConfig{
		Name: "vault", URL: deadMCPURL, TimeoutSeconds: 1, Required: true,
	}))

	status, body := getReadyz(t, env)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: a required MCP server that is down must gate readiness (%+v)", status, body)
	}
	if body.Status != "not_ready" {
		t.Errorf("status field = %q, want not_ready", body.Status)
	}
	if body.Reason != "required mcp server unavailable" {
		t.Errorf("reason = %q, want %q", body.Reason, "required mcp server unavailable")
	}
	if body.Reason != "" && len(body.MCPServers) != 0 {
		t.Errorf("a 503 body carried mcp_servers %+v; the not-ready body names only the reason", body.MCPServers)
	}
}

// A gateway with no MCP servers configured omits the key entirely rather than
// serving an empty list, so a monitor can tell "no MCP" from "MCP is down".
func TestReadyz_NoMCPServersOmitsTheKey(t *testing.T) {
	env := newTestServer(t)

	status, body := getReadyz(t, env)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if len(body.MCPServers) != 0 {
		t.Errorf("mcp_servers = %+v, want the key absent when none are configured", body.MCPServers)
	}
}

// A server whose transport cannot even be built — an unresolvable ${VAR} in its
// headers — is registered as unready rather than dropped. A dropped server
// marked required would leave /readyz answering ready while the dependency it
// names was silently missing from the body.
func TestReadyz_MCPServerWithUnresolvableEnvRefIsReportedUnready(t *testing.T) {
	env := newTestServer(t, withMCPServers(mcp.ServerConfig{
		Name:           "broken-ref",
		URL:            deadMCPURL,
		Headers:        map[string]string{"Authorization": "Bearer ${FERRO_TEST_UNSET_MCP_TOKEN}"},
		TimeoutSeconds: 1,
	}))

	status, body := getReadyz(t, env)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if len(body.MCPServers) != 1 {
		t.Fatalf("mcp_servers = %+v, want the unregistrable server still reported", body.MCPServers)
	}
	if got := body.MCPServers[0]; got.Name != "broken-ref" || got.Ready {
		t.Errorf("mcp_servers[0] = %+v, want {broken-ref false false}", got)
	}
}
