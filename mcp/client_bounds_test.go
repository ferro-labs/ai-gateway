package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

// TestClient_RejectsOversizedResponseBody verifies the success-path read is
// bounded: an MCP server (an untrusted-content boundary) returning a body larger
// than maxResponseBodyBytes is rejected with a clear error rather than being read
// unbounded into memory.
func TestClient_RejectsOversizedResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// One byte past the cap so the bounded read detects the overflow.
		_, _ = w.Write(make([]byte, maxResponseBodyBytes+1))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, nil, 5*time.Second)
	_, err := c.Initialize(context.Background())
	if err == nil {
		t.Fatal("expected an error for an oversized MCP response body, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected a size-limit error, got: %v", err)
	}
}

// TestClient_SSEFrameLimitMatchesBodyLimit is the regression guard for a
// Streamable HTTP client that read the body under a 10 MiB cap and then parsed
// it with the shared provider-streaming SSE scanner, which caps ONE line at
// 1 MiB. An MCP result is a single JSON document on a single data: line, and a
// tool returning an embedded image or a large file read routinely exceeds 1 MiB
// — so the identical result succeeded when the server answered with JSON and
// failed with "mcp read sse" when it answered with SSE, which is the server's
// choice to make, not the client's.
func TestClient_SSEFrameLimitMatchesBodyLimit(t *testing.T) {
	// Comfortably past the 1 MiB frame cap, comfortably inside the body cap.
	big := strings.Repeat("x", 3<<20)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req JSONRPCRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", mustMarshal(JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  mustMarshal(ToolCallResult{Content: []ContentBlock{{Type: "text", Text: big}}}),
		}))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, nil, 30*time.Second)
	result, err := c.CallTool(context.Background(), "read_file", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("CallTool over SSE with a %d-byte result: %v", len(big), err)
	}
	if len(result.Content) != 1 || len(result.Content[0].Text) != len(big) {
		t.Fatalf("result truncated: got %d content blocks", len(result.Content))
	}
}

// TestClient_SSEMultiLineDataField covers the other half of the same decode: per
// the SSE spec an event's data field may be split across several data: lines,
// which are joined with newlines before the event is dispatched. Decoding each
// line on its own rejected every one of them and reported the response as
// carrying no result.
func TestClient_SSEMultiLineDataField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req JSONRPCRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "text/event-stream")

		// One JSON document, pretty-printed, so each of its lines is its own
		// data: line — exactly what the spec says a server may send.
		doc, err := json.MarshalIndent(JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  mustMarshal(ToolCallResult{Content: []ContentBlock{{Type: "text", Text: "split"}}}),
		}, "", "  ")
		if err != nil {
			return
		}
		_, _ = io.WriteString(w, "event: message\n")
		for _, line := range strings.Split(string(doc), "\n") {
			_, _ = fmt.Fprintf(w, "data: %s\n", line)
		}
		_, _ = io.WriteString(w, "\n")
	}))
	defer srv.Close()

	c := NewClient(srv.URL, nil, 5*time.Second)
	result, err := c.CallTool(context.Background(), "read_file", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("CallTool over an SSE event with a multi-line data field: %v", err)
	}
	if len(result.Content) != 1 || result.Content[0].Text != "split" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

// TestClient_AdvertisesLibraryProtocolVersion pins the HTTP transport to the
// same protocol revision the stdio transport hands to mark3labs. A literal here
// silently diverged from the library on every dependency bump.
func TestClient_AdvertisesLibraryProtocolVersion(t *testing.T) {
	var advertised string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req JSONRPCRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &params)
		if req.Method == mcpMethodInitialize {
			advertised = params.ProtocolVersion
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  mustMarshal(map[string]any{"protocolVersion": mcpgo.LATEST_PROTOCOL_VERSION}),
		})
	}))
	defer srv.Close()

	info, err := NewClient(srv.URL, nil, 5*time.Second).Initialize(context.Background())
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if advertised != mcpgo.LATEST_PROTOCOL_VERSION {
		t.Errorf("advertised protocolVersion = %q, want the library constant %q",
			advertised, mcpgo.LATEST_PROTOCOL_VERSION)
	}
	if info.ProtocolVersion != mcpgo.LATEST_PROTOCOL_VERSION {
		t.Errorf("ServerInfo.ProtocolVersion = %q, want the server's negotiated %q",
			info.ProtocolVersion, mcpgo.LATEST_PROTOCOL_VERSION)
	}
}

// TestClient_UnknownProtocolVersionIsNotFatal pins the chosen policy: a revision
// this build does not list is a warning, never a rejection. Rejecting would take
// a working deployment offline the moment a server upgraded ahead of the pinned
// library, and the surfaces the gateway uses — tools/list and tools/call — have
// been stable across every revision.
func TestClient_UnknownProtocolVersionIsNotFatal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req JSONRPCRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  mustMarshal(map[string]any{"protocolVersion": "2099-01-01"}),
		})
	}))
	defer srv.Close()

	info, err := NewClient(srv.URL, nil, 5*time.Second).Initialize(context.Background())
	if err != nil {
		t.Fatalf("Initialize against a server naming an unknown protocol version = %v; "+
			"an unrecognised revision must warn and continue, not fail the handshake", err)
	}
	if info.ProtocolVersion != "2099-01-01" {
		t.Errorf("ProtocolVersion = %q, want the version the server actually named", info.ProtocolVersion)
	}
}
