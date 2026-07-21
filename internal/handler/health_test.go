package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
	mcpconfig "github.com/ferro-labs/ai-gateway/mcp"
	"github.com/ferro-labs/ai-gateway/providers"
)

func TestHealthStatusCodes(t *testing.T) {
	tests := []struct {
		name       string
		register   bool
		wantStatus string
		wantCode   int
	}{
		{name: "healthy", register: true, wantStatus: "ok", wantCode: http.StatusOK},
		{name: "no providers", register: false, wantStatus: "no_providers", wantCode: http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gw, err := newTestGateway(t, aigateway.Config{
				Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
				Targets:  []aigateway.Target{{VirtualKey: "health-provider"}},
			})

			if err != nil {
				t.Fatalf("New gateway: %v", err)
			}
			if tt.register {
				gw.RegisterProvider(healthProvider{})
			}

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", nil)
			w := httptest.NewRecorder()
			Health(gw).ServeHTTP(w, req)

			if w.Code != tt.wantCode {
				t.Fatalf("status code = %d, want %d: %s", w.Code, tt.wantCode, w.Body.String())
			}
			var payload struct {
				Status string `json:"status"`
			}
			if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
				t.Fatalf("decode health response: %v", err)
			}
			if payload.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", payload.Status, tt.wantStatus)
			}
		})
	}
}

func TestLivez(t *testing.T) {
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/livez", nil)
	w := httptest.NewRecorder()
	Livez().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", w.Code, http.StatusOK)
	}
	var payload struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode livez response: %v", err)
	}
	if payload.Status != "ok" {
		t.Fatalf("status = %q, want %q", payload.Status, "ok")
	}
}

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func TestReadyz(t *testing.T) {
	tests := []struct {
		name         string
		register     bool
		pingers      []Pinger
		wantCode     int
		wantStatus   string
		wantReasonIn string
	}{
		{
			name:       "ready with reachable stores",
			register:   true,
			pingers:    []Pinger{fakePinger{}, fakePinger{}},
			wantCode:   http.StatusOK,
			wantStatus: "ready",
		},
		{
			name:         "store unreachable",
			register:     true,
			pingers:      []Pinger{fakePinger{}, fakePinger{err: errors.New("connection refused")}},
			wantCode:     http.StatusServiceUnavailable,
			wantStatus:   "not_ready",
			wantReasonIn: "store unreachable",
		},
		{
			name:         "no ready providers",
			register:     false,
			pingers:      []Pinger{fakePinger{}},
			wantCode:     http.StatusServiceUnavailable,
			wantStatus:   "not_ready",
			wantReasonIn: "no ready providers",
		},
		{
			name:       "nil pinger is skipped",
			register:   true,
			pingers:    []Pinger{nil},
			wantCode:   http.StatusOK,
			wantStatus: "ready",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gw, err := newTestGateway(t, aigateway.Config{
				Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
				Targets:  []aigateway.Target{{VirtualKey: "health-provider"}},
			})

			if err != nil {
				t.Fatalf("New gateway: %v", err)
			}
			if tt.register {
				gw.RegisterProvider(healthProvider{})
			}

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil)
			w := httptest.NewRecorder()
			Readyz(gw, tt.pingers...).ServeHTTP(w, req)

			if w.Code != tt.wantCode {
				t.Fatalf("status code = %d, want %d: %s", w.Code, tt.wantCode, w.Body.String())
			}
			var payload struct {
				Status string `json:"status"`
				Reason string `json:"reason"`
			}
			if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
				t.Fatalf("decode readyz response: %v", err)
			}
			if payload.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", payload.Status, tt.wantStatus)
			}
			if tt.wantReasonIn != "" && !strings.Contains(payload.Reason, tt.wantReasonIn) {
				t.Fatalf("reason = %q, want substring %q", payload.Reason, tt.wantReasonIn)
			}
		})
	}
}

func TestReadyzNilGateway(t *testing.T) {
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	Readyz(nil).ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status code = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}

func TestReadyzDoesNotLeakStoreErrorDetail(t *testing.T) {
	gw, err := newTestGateway(t, aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: "health-provider"}},
	})

	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}
	gw.RegisterProvider(healthProvider{})

	//nolint:gosec // G101: a fake DSN; the point of the test is that /readyz never echoes it
	const secret = "postgres://admin:hunter2@db.internal:5432/gateway"
	pinger := fakePinger{err: errors.New("dial tcp: " + secret + ": connection refused")}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	Readyz(gw, pinger).ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status code = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
	if strings.Contains(w.Body.String(), secret) {
		t.Fatalf("response body leaked store error detail: %s", w.Body.String())
	}
}

type healthProvider struct{}

func (healthProvider) Name() string              { return "health-provider" }
func (healthProvider) SupportedModels() []string { return []string{"health-model"} }
func (healthProvider) SupportsModel(model string) bool {
	return model == "health-model"
}
func (healthProvider) Models() []providers.ModelInfo {
	return []providers.ModelInfo{{ID: "health-model", Object: "model", OwnedBy: "health-provider"}}
}
func (healthProvider) Complete(context.Context, providers.Request) (*providers.Response, error) {
	return nil, nil
}

// unreachableMCP is a server config whose transport can never come up, so the
// registry leaves it unready — the state a crashed or misconfigured MCP server
// produces.
func unreachableMCP(name string, required bool) mcpconfig.ServerConfig {
	return mcpconfig.ServerConfig{
		Name:           name,
		URL:            "http://127.0.0.1:1/mcp",
		TimeoutSeconds: 1,
		Required:       required,
	}
}

// TestReadyzMCPGating covers the readiness contract for MCP servers: an
// unready server gates /readyz only when it opted in via `required`.
//
// The default matters as much as the feature. Making every MCP server gate
// readiness would take a pod out of rotation on upgrade — stopping all LLM
// traffic, including requests that never touch a tool — for anyone who had one
// configured. So absent `required` must behave exactly as before.
func TestReadyzMCPGating(t *testing.T) {
	tests := []struct {
		name         string
		servers      []mcpconfig.ServerConfig
		wantCode     int
		wantStatus   string
		wantReasonIn string
	}{
		{
			name:       "no mcp servers configured is unaffected",
			wantCode:   http.StatusOK,
			wantStatus: "ready",
		},
		{
			name:       "unready server without required does not gate",
			servers:    []mcpconfig.ServerConfig{unreachableMCP("optional-srv", false)},
			wantCode:   http.StatusOK,
			wantStatus: "ready",
		},
		{
			name:         "unready server with required gates",
			servers:      []mcpconfig.ServerConfig{unreachableMCP("critical-srv", true)},
			wantCode:     http.StatusServiceUnavailable,
			wantStatus:   "not_ready",
			wantReasonIn: "required mcp server unavailable",
		},
		{
			name: "one required server down among optional ones gates",
			servers: []mcpconfig.ServerConfig{
				unreachableMCP("optional-a", false),
				unreachableMCP("critical-b", true),
				unreachableMCP("optional-c", false),
			},
			wantCode:     http.StatusServiceUnavailable,
			wantStatus:   "not_ready",
			wantReasonIn: "required mcp server unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gw, err := newTestGateway(t, aigateway.Config{
				Strategy:   aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
				Targets:    []aigateway.Target{{VirtualKey: "health-provider"}},
				MCPServers: tt.servers,
			})
			if err != nil {
				t.Fatalf("New gateway: %v", err)
			}
			gw.RegisterProvider(healthProvider{})

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil)
			w := httptest.NewRecorder()
			Readyz(gw, fakePinger{}).ServeHTTP(w, req)

			if w.Code != tt.wantCode {
				t.Fatalf("status code = %d, want %d: %s", w.Code, tt.wantCode, w.Body.String())
			}
			var payload struct {
				Status string `json:"status"`
				Reason string `json:"reason"`
			}
			if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
				t.Fatalf("decode readyz response: %v", err)
			}
			if payload.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", payload.Status, tt.wantStatus)
			}
			if tt.wantReasonIn != "" && !strings.Contains(payload.Reason, tt.wantReasonIn) {
				t.Fatalf("reason = %q, want substring %q", payload.Reason, tt.wantReasonIn)
			}
		})
	}
}

// TestReadyzGatesOnAServerThatNeverRegistered is the regression test for a
// required MCP server that fails before it can be registered at all.
//
// A server whose headers or environment reference an undefined variable is
// resolved before its transport exists, so it never reaches the registry.
// Readiness reads the registry, so the server was not merely reported unready —
// it was absent from the response entirely, and /readyz answered 200 ready
// while a server the operator had marked `required` had never been attempted.
// The mixed fleet here is the worst case: mcp_servers is populated, so the body
// looks complete while quietly omitting exactly the server that gates it.
func TestReadyzGatesOnAServerThatNeverRegistered(t *testing.T) {
	// The fixture's whole premise is that the variable does not resolve.
	const undefinedVar = "FERRO_HANDLER_TEST_UNDEFINED_MCP_VAR"
	if _, set := os.LookupEnv(undefinedVar); set {
		t.Skipf("%s is set; this fixture requires it to be undefined", undefinedVar)
	}

	gw, err := newTestGateway(t, aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: "health-provider"}},
		MCPServers: []mcpconfig.ServerConfig{
			unreachableMCP("optional-srv", false),
			{
				Name:     "vault",
				Command:  "true",
				Required: true,
				Env:      map[string]string{"TOKEN": "${" + undefinedVar + "}"},
			},
		},
	})
	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}
	gw.RegisterProvider(healthProvider{})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	Readyz(gw, fakePinger{}).ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status code = %d, want 503: a required server that never registered left the gateway reporting ready: %s",
			w.Code, w.Body.String())
	}

	var payload struct {
		Status     string `json:"status"`
		Reason     string `json:"reason"`
		MCPServers []struct {
			Name     string `json:"name"`
			Ready    bool   `json:"ready"`
			Required bool   `json:"required"`
		} `json:"mcp_servers"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode readyz response: %v", err)
	}
	if !strings.Contains(payload.Reason, "required mcp server unavailable") {
		t.Errorf("reason = %q, want the required-server reason", payload.Reason)
	}

	// The 503 body carries no mcp_servers, so a second gateway proves the
	// server is reported as configured rather than silently dropped.
	readiness := gw.Readiness()
	var found bool
	for _, s := range readiness.MCPServers {
		if s.Name != "vault" {
			continue
		}
		found = true
		if s.Ready {
			t.Error("a server that never got a transport is reported ready")
		}
		if !s.Required {
			t.Error("the server lost its Required flag, so nothing would gate on it")
		}
		if s.LastError == "" {
			t.Error("no reason recorded for a server that never registered")
		}
	}
	if !found {
		t.Errorf("MCPServers = %+v, want an entry for the configured server %q", readiness.MCPServers, "vault")
	}
}

// TestReadyzReportsMCPStateWithoutGating proves the observability half: MCP
// state appears in the body for servers that do not gate readiness, so an
// operator can watch MCP health without having to risk their rollout on it.
func TestReadyzReportsMCPStateWithoutGating(t *testing.T) {
	gw, err := newTestGateway(t, aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: "health-provider"}},
		MCPServers: []mcpconfig.ServerConfig{
			unreachableMCP("watch-me", false),
		},
	})
	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}
	gw.RegisterProvider(healthProvider{})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	Readyz(gw, fakePinger{}).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200: %s", w.Code, w.Body.String())
	}
	var payload struct {
		Status     string `json:"status"`
		MCPServers []struct {
			Name     string `json:"name"`
			Ready    bool   `json:"ready"`
			Required bool   `json:"required"`
		} `json:"mcp_servers"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode readyz response: %v", err)
	}
	if len(payload.MCPServers) != 1 {
		t.Fatalf("mcp_servers = %+v, want one entry", payload.MCPServers)
	}
	got := payload.MCPServers[0]
	if got.Name != "watch-me" || got.Ready || got.Required {
		t.Errorf("mcp_servers[0] = %+v, want {watch-me false false}", got)
	}

	// /readyz is unauthenticated and an MCP failure can quote a URL, an
	// authorization header, or a subprocess command line. The reason is logged,
	// never served.
	if body := w.Body.String(); strings.Contains(body, "127.0.0.1:1") {
		t.Errorf("readyz body leaked the MCP server address: %s", body)
	}
	if body := w.Body.String(); strings.Contains(strings.ToLower(body), "last_error") {
		t.Errorf("readyz body exposed an error detail field: %s", body)
	}
}
