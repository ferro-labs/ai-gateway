package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// newMockServer builds a minimal MCP server exposing the given tools.
func newMockServer(t *testing.T, tools []Tool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad req", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case mcpMethodInitialize:
			w.Header().Set("Mcp-Session-Id", "sid-registry-test")
			_ = json.NewEncoder(w).Encode(JSONRPCResponse{
				JSONRPC: "2.0", ID: req.ID,
				Result: mustMarshal(ServerInfo{Name: "mock", Version: "1"}),
			})
		case mcpMethodToolsList:
			_ = json.NewEncoder(w).Encode(JSONRPCResponse{
				JSONRPC: "2.0", ID: req.ID,
				Result: mustMarshal(map[string]any{"tools": tools}),
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestRegistryRegisterAndAllTools(t *testing.T) {
	tools := []Tool{{Name: "ta"}, {Name: "tb"}}
	srv := newMockServer(t, tools)
	defer srv.Close()

	reg := NewRegistry()
	reg.RegisterConfig(ServerConfig{Name: "s1", URL: srv.URL, TimeoutSeconds: 5})

	if !reg.HasServers() {
		t.Fatal("expected HasServers true after RegisterConfig")
	}
	if reg.IsReady("s1") {
		t.Fatal("expected not ready before InitializeAll")
	}

	reg.InitializeAll(context.Background(), func(name string, err error) {
		t.Errorf("unexpected init error for %s: %v", name, err)
	})

	if !reg.IsReady("s1") {
		t.Fatal("expected IsReady true after InitializeAll")
	}

	all := reg.AllTools()
	if len(all) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(all))
	}
}

func TestRegistryFindToolServer(t *testing.T) {
	tools := []Tool{{Name: "find_me"}}
	srv := newMockServer(t, tools)
	defer srv.Close()

	reg := NewRegistry()
	reg.RegisterConfig(ServerConfig{Name: "finder", URL: srv.URL, TimeoutSeconds: 5})
	reg.InitializeAll(context.Background(), nil)

	client, ok := reg.FindToolServer("find_me")
	if !ok || client == nil {
		t.Fatal("expected to find tool server for 'find_me'")
	}
	_, ok2 := reg.FindToolServer("no_such_tool")
	if ok2 {
		t.Fatal("expected not-found for unknown tool")
	}
}

func TestRegistryAllowedToolsFilter(t *testing.T) {
	tools := []Tool{{Name: "allowed"}, {Name: "blocked"}}
	srv := newMockServer(t, tools)
	defer srv.Close()

	reg := NewRegistry()
	reg.RegisterConfig(ServerConfig{
		Name:           "filtered",
		URL:            srv.URL,
		AllowedTools:   []string{"allowed"},
		TimeoutSeconds: 5,
	})
	reg.InitializeAll(context.Background(), nil)

	all := reg.AllTools()
	if len(all) != 1 {
		t.Fatalf("expected 1 tool after filter, got %d: %v", len(all), all)
	}
	if all[0].Name != "allowed" {
		t.Errorf("unexpected tool: %q", all[0].Name)
	}

	// blocked tool must not be routable
	_, ok := reg.FindToolServer("blocked")
	if ok {
		t.Error("blocked tool should not be in registry")
	}
}

func TestRegistryInitializeAllIsIdempotent(t *testing.T) {
	var callCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req JSONRPCRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if req.Method == mcpMethodInitialize {
			callCount++
			w.Header().Set("Mcp-Session-Id", "sid-idem")
			_ = json.NewEncoder(w).Encode(JSONRPCResponse{
				JSONRPC: "2.0", ID: req.ID,
				Result: mustMarshal(ServerInfo{Name: "m", Version: "1"}),
			})
			return
		}
		_ = json.NewEncoder(w).Encode(JSONRPCResponse{
			JSONRPC: "2.0", ID: req.ID,
			Result: mustMarshal(map[string]any{"tools": []Tool{}}),
		})
	}))
	defer srv.Close()

	reg := NewRegistry()
	reg.RegisterConfig(ServerConfig{Name: "idem", URL: srv.URL, TimeoutSeconds: 5})
	reg.InitializeAll(context.Background(), nil)
	reg.InitializeAll(context.Background(), nil) // second call must be a no-op

	if callCount != 1 {
		t.Errorf("expected initialize called once, got %d", callCount)
	}
}

func TestRegistryConcurrentAccess(t *testing.T) {
	tools := []Tool{{Name: "conc"}}
	srv := newMockServer(t, tools)
	defer srv.Close()

	reg := NewRegistry()
	reg.RegisterConfig(ServerConfig{Name: "c1", URL: srv.URL, TimeoutSeconds: 5})
	reg.InitializeAll(context.Background(), nil)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = reg.AllTools()
			_, _ = reg.FindToolServer("conc")
			_ = reg.IsReady("c1")
			_ = reg.HasServers()
		}()
	}
	wg.Wait()
}

func TestRegistryServerNames(t *testing.T) {
	reg := NewRegistry()
	reg.RegisterConfig(ServerConfig{Name: "alpha", URL: "http://a", TimeoutSeconds: 1})
	reg.RegisterConfig(ServerConfig{Name: "beta", URL: "http://b", TimeoutSeconds: 1})

	names := reg.ServerNames()
	if len(names) != 2 {
		t.Fatalf("expected 2 server names, got %d: %v", len(names), names)
	}
}

func TestRegistryInitError(t *testing.T) {
	// Server that always returns 500
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "die", http.StatusInternalServerError)
	}))
	defer srv.Close()

	reg := NewRegistry()
	reg.RegisterConfig(ServerConfig{Name: "bad", URL: srv.URL, TimeoutSeconds: 2})

	var gotErr error
	reg.InitializeAll(context.Background(), func(_ string, err error) {
		gotErr = err
	})

	if gotErr == nil {
		t.Error("expected init error for 500 server")
	}
	if reg.IsReady("bad") {
		t.Error("server should not be ready after init error")
	}
}

func TestRegistryReRegisterRemovesStaleToolMapEntries(t *testing.T) {
	// First server advertises tools [old_tool, shared_tool].
	// After re-registration with AllowedTools=["shared_tool"], old_tool must
	// be removed from toolMap so FindToolServer no longer routes to it.
	allTools := []Tool{{Name: "old_tool"}, {Name: "shared_tool"}}
	srv := newMockServer(t, allTools)
	defer srv.Close()

	reg := NewRegistry()
	reg.RegisterConfig(ServerConfig{Name: "reregister-srv", URL: srv.URL, TimeoutSeconds: 5})
	reg.InitializeAll(context.Background(), nil)

	if _, ok := reg.FindToolServer("old_tool"); !ok {
		t.Fatal("old_tool should be routable before re-registration")
	}

	// Re-register the same server name but restrict to shared_tool only.
	reg.RegisterConfig(ServerConfig{
		Name:           "reregister-srv",
		URL:            srv.URL,
		AllowedTools:   []string{"shared_tool"},
		TimeoutSeconds: 5,
	})
	reg.InitializeAll(context.Background(), nil)

	if _, ok := reg.FindToolServer("old_tool"); ok {
		t.Error("old_tool must not be routable after re-registration with restricted AllowedTools")
	}
	if _, ok := reg.FindToolServer("shared_tool"); !ok {
		t.Error("shared_tool must still be routable after re-registration")
	}
}
