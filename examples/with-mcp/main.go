// Package main demonstrates Ferro Labs AI Gateway's MCP (Model Context Protocol)
// integration. The gateway automatically discovers tools from connected MCP servers,
// injects them into every LLM request, and drives an agentic tool-call loop when
// the model responds with tool_calls — all without any changes to your client code.
//
// This example:
//   - Starts a local mock MCP server that exposes one tool: get_weather
//   - Configures the gateway to connect to that MCP server
//   - Sends a weather question to the LLM
//   - The gateway injects the get_weather tool definition, then resolves any
//     tool_call responses via the MCP server and re-routes automatically
//   - Prints the final LLM answer
//
// OPENAI_API_KEY=sk-...        go run ./examples/with-mcp
// ANTHROPIC_API_KEY=sk-ant-... go run ./examples/with-mcp
// GROQ_API_KEY=gsk_...        go run ./examples/with-mcp
//
// Models that support function/tool calling produce the most compelling output
// (gpt-4o, claude-3-5-sonnet, llama-3.3-70b-versatile, etc.).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/mcp"
	"github.com/ferro-labs/ai-gateway/providers"

	anthropicpkg "github.com/ferro-labs/ai-gateway/providers/anthropic"
	coherepkg "github.com/ferro-labs/ai-gateway/providers/cohere"
	deepseekpkg "github.com/ferro-labs/ai-gateway/providers/deepseek"
	geminipkg "github.com/ferro-labs/ai-gateway/providers/gemini"
	groqpkg "github.com/ferro-labs/ai-gateway/providers/groq"
	mistralpkg "github.com/ferro-labs/ai-gateway/providers/mistral"
	openaipkg "github.com/ferro-labs/ai-gateway/providers/openai"
	togetherpkg "github.com/ferro-labs/ai-gateway/providers/together"
)

func main() {
	// 1. Start a local mock MCP server that advertises a get_weather tool.
	mcpAddr, stopMCP := startMockMCPServer()
	defer stopMCP()
	mcpURL := "http://" + mcpAddr + "/mcp"
	fmt.Printf("Mock MCP server listening at %s\n", mcpURL)

	// 2. Pick a provider from any available API key in the environment.
	provider := firstProvider()
	model := provider.SupportedModels()[0]
	fmt.Printf("Provider: %s  Model: %s\n\n", provider.Name(), model)

	// 3. Build the gateway with the MCP server wired in.
	//    The gateway initialises the server (initialize + tools/list) during New().
	gw, err := aigateway.New(aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: provider.Name()}},
		MCPServers: []mcp.ServerConfig{
			{
				Name:           "weather-server",
				URL:            mcpURL,
				MaxCallDepth:   3,  // stop after 3 agentic iterations
				TimeoutSeconds: 15, // per-call timeout
			},
		},
	})
	if err != nil {
		stopMCP()
		log.Fatalf("Failed to create gateway: %v", err) //nolint:gocritic
	}
	gw.RegisterProvider(provider)

	// 4. Route the request. The gateway:
	//    a) Injects the get_weather tool definition into the LLM request.
	//    b) If the LLM responds with tool_calls, resolves each call via the
	//       MCP server and re-routes with the tool result appended.
	//    c) Returns the final assistant message once the loop is complete.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	req := providers.Request{
		Model: model,
		Messages: []providers.Message{
			{Role: "user", Content: "What is the current weather in San Francisco? Please use the available tool."},
		},
		MaxTokens: intPtr(256),
	}

	fmt.Println("Sending request (agentic MCP loop active)...")
	resp, err := gw.Route(ctx, req)
	if err != nil {
		cancel()
		log.Printf("Request failed: %v", err)
		os.Exit(1) //nolint:gocritic
	}

	out, _ := json.MarshalIndent(resp, "", "  ")
	fmt.Println(string(out))
}

// ─── Mock MCP Server ─────────────────────────────────────────────────────────

// startMockMCPServer starts a local HTTP server that speaks the MCP 2025-11-25
// Streamable HTTP transport. It exposes one tool: get_weather.
// Returns the listener address and a stop function.
func startMockMCPServer() (addr string, stop func()) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", mcpHandler)
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	return ln.Addr().String(), func() { _ = srv.Close() }
}

// mcpHandler handles all MCP JSON-RPC 2.0 requests at /mcp.
func mcpHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close() //nolint:errcheck

	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	switch req.Method {
	case "initialize":
		// Set the session ID header required by the spec.
		w.Header().Set("Mcp-Session-Id", "mock-session-001")
		writeJSON(w, rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: mustMarshal(map[string]any{
				"name":    "weather-server",
				"version": "1.0.0",
				"capabilities": map[string]any{
					"tools": map[string]any{"listChanged": false},
				},
			}),
		})

	case "tools/list":
		// Advertise the get_weather tool with a JSON Schema for its arguments.
		schema := mustMarshal(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"location": map[string]any{
					"type":        "string",
					"description": "City and state, e.g. San Francisco, CA",
				},
				"unit": map[string]any{
					"type":        "string",
					"enum":        []string{"celsius", "fahrenheit"},
					"description": "Temperature unit",
				},
			},
			"required": []string{"location"},
		})
		writeJSON(w, rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: mustMarshal(map[string]any{
				"tools": []map[string]any{
					{
						"name":        "get_weather",
						"description": "Get the current weather conditions for a location.",
						"inputSchema": json.RawMessage(schema),
					},
				},
			}),
		})

	case "tools/call":
		// Parse the tool call arguments and return a fake weather result.
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &params)

		var args struct {
			Location string `json:"location"`
			Unit     string `json:"unit"`
		}
		_ = json.Unmarshal(params.Arguments, &args)

		if args.Location == "" {
			args.Location = "San Francisco, CA"
		}
		if args.Unit == "" {
			args.Unit = "fahrenheit"
		}

		temp := 62
		if args.Unit == "celsius" {
			temp = 17
		}
		result := fmt.Sprintf(
			`{"location":%q,"temperature":%d,"unit":%q,"condition":"Partly Cloudy","humidity":72,"wind_mph":12}`,
			args.Location, temp, args.Unit,
		)
		fmt.Printf("[mock MCP] tools/call %s(%s) → %s\n", params.Name, string(params.Arguments), result)

		writeJSON(w, rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: mustMarshal(map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": result},
				},
				"isError": false,
			}),
		})

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// ─── Types & helpers ─────────────────────────────────────────────────────────

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
}

func writeJSON(w http.ResponseWriter, v any) {
	_ = json.NewEncoder(w).Encode(v)
}

func mustMarshal(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func intPtr(i int) *int { return &i }

// firstProvider returns the first provider for which an API key is set.
func firstProvider() providers.Provider {
	type entry struct {
		env    string
		create func(key string) (providers.Provider, error)
	}
	candidates := []entry{
		{"OPENAI_API_KEY", func(k string) (providers.Provider, error) { return openaipkg.New(k, "") }},
		{"ANTHROPIC_API_KEY", func(k string) (providers.Provider, error) { return anthropicpkg.New(k, "") }},
		{"GROQ_API_KEY", func(k string) (providers.Provider, error) { return groqpkg.New(k, "") }},
		{"GEMINI_API_KEY", func(k string) (providers.Provider, error) { return geminipkg.New(k, "") }},
		{"MISTRAL_API_KEY", func(k string) (providers.Provider, error) { return mistralpkg.New(k, "") }},
		{"TOGETHER_API_KEY", func(k string) (providers.Provider, error) { return togetherpkg.New(k, "") }},
		{"COHERE_API_KEY", func(k string) (providers.Provider, error) { return coherepkg.New(k, "") }},
		{"DEEPSEEK_API_KEY", func(k string) (providers.Provider, error) { return deepseekpkg.New(k, "") }},
	}
	for _, c := range candidates {
		if key := os.Getenv(c.env); key != "" {
			p, err := c.create(key)
			if err != nil {
				log.Fatalf("Failed to create provider for %s: %v", c.env, err)
			}
			return p
		}
	}
	log.Fatal("No provider API key set. Set at least one of: OPENAI_API_KEY, ANTHROPIC_API_KEY, GROQ_API_KEY, GEMINI_API_KEY, MISTRAL_API_KEY, TOGETHER_API_KEY, COHERE_API_KEY, DEEPSEEK_API_KEY")
	return nil
}
