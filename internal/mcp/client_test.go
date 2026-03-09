package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// buildMockMCPServer creates an httptest.Server that speaks a minimal MCP
// Streamable HTTP protocol. It handles:
//   - POST / with method "initialize"    → returns ServerInfo
//   - POST / with method "tools/list"    → returns two tools
//   - POST / with method "tools/call"    → returns a text ContentBlock
func buildMockMCPServer(t *testing.T) *httptest.Server {
	t.Helper()
	var sessionMu sync.Mutex
	sessionIDs := map[string]bool{}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		switch req.Method {
		case "initialize":
			sid := "test-session-123"
			sessionMu.Lock()
			sessionIDs[sid] = true
			sessionMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Mcp-Session-Id", sid)
			resp := JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: mustMarshal(ServerInfo{
					Name:    "mock",
					Version: "0.1",
					Capabilities: Capabilities{
						Tools: &ToolsCapability{ListChanged: false},
					},
				}),
			}
			_ = json.NewEncoder(w).Encode(resp)

		case "tools/list":
			tools := []Tool{
				{Name: "read_file", Description: "Read a file"},
				{Name: "write_file", Description: "Write a file"},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  mustMarshal(map[string]any{"tools": tools}),
			})

		case "tools/call":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  mustMarshal(ToolCallResult{Content: []ContentBlock{{Type: "text", Text: "hello"}}}),
			})

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// ---------------------------------------------------------------------------

func TestClientInitialize(t *testing.T) {
	srv := buildMockMCPServer(t)
	defer srv.Close()

	c := NewClient(srv.URL, nil, 5*time.Second)
	info, err := c.Initialize(context.Background())
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if info.Name != "mock" {
		t.Errorf("unexpected server name: %q", info.Name)
	}
	if c.getSessionID() == "" {
		t.Error("expected session ID to be set after initialize")
	}
}

func TestClientListTools(t *testing.T) {
	srv := buildMockMCPServer(t)
	defer srv.Close()

	c := NewClient(srv.URL, nil, 5*time.Second)
	if _, err := c.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	if tools[0].Name != "read_file" {
		t.Errorf("unexpected tool name: %q", tools[0].Name)
	}
}

func TestClientCallTool(t *testing.T) {
	srv := buildMockMCPServer(t)
	defer srv.Close()

	c := NewClient(srv.URL, nil, 5*time.Second)
	if _, err := c.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	result, err := c.CallTool(context.Background(), "read_file", json.RawMessage(`{"path":"/tmp/x"}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(result.Content) == 0 || result.Content[0].Text != "hello" {
		t.Errorf("unexpected result: %+v", result)
	}
}

func TestClientHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, nil, 5*time.Second)
	_, err := c.Initialize(context.Background())
	if err == nil {
		t.Fatal("expected error for HTTP 503, got nil")
	}
}

func TestClientSessionIDPropagation(t *testing.T) {
	var receivedSID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedSID = r.Header.Get("Mcp-Session-Id")
		var req JSONRPCRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if req.Method == "initialize" {
			w.Header().Set("Mcp-Session-Id", "propagated-sid")
			_ = json.NewEncoder(w).Encode(JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  mustMarshal(ServerInfo{Name: "x", Version: "1"}),
			})
			return
		}
		// second request
		_ = json.NewEncoder(w).Encode(JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  mustMarshal(map[string]any{"tools": []Tool{}}),
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, nil, 5*time.Second)
	if _, err := c.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	receivedSID = "" // reset before second request
	_, _ = c.ListTools(context.Background())
	if receivedSID != "propagated-sid" {
		t.Errorf("expected session ID to be sent on subsequent calls, got %q", receivedSID)
	}
}

func TestClientConcurrentSafety(t *testing.T) {
	srv := buildMockMCPServer(t)
	defer srv.Close()

	c := NewClient(srv.URL, nil, 5*time.Second)
	if _, err := c.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.ListTools(context.Background())
		}()
	}
	wg.Wait()
}
