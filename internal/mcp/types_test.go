package mcp

import (
	"encoding/json"
	"testing"
)

func TestMCPServerConfigDefaults(t *testing.T) {
	cfg := ServerConfig{
		Name: "test",
		URL:  "http://localhost:8080",
	}
	if cfg.MaxCallDepth != 0 {
		t.Errorf("expected zero MaxCallDepth, got %d", cfg.MaxCallDepth)
	}
	if cfg.TimeoutSeconds != 0 {
		t.Errorf("expected zero TimeoutSeconds, got %d", cfg.TimeoutSeconds)
	}
}

func TestMCPServerConfigRoundTrip(t *testing.T) {
	cfg := ServerConfig{
		Name:           "myserver",
		URL:            "http://mcp.example.com",
		Headers:        map[string]string{"Authorization": "Bearer tok"},
		AllowedTools:   []string{"read_file", "write_file"},
		MaxCallDepth:   3,
		TimeoutSeconds: 20,
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got ServerConfig
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Name != cfg.Name || got.URL != cfg.URL {
		t.Errorf("name/url mismatch: got %+v", got)
	}
	if len(got.AllowedTools) != 2 || got.AllowedTools[0] != "read_file" {
		t.Errorf("AllowedTools mismatch: %v", got.AllowedTools)
	}
	if got.MaxCallDepth != cfg.MaxCallDepth {
		t.Errorf("MaxCallDepth mismatch: got %d", got.MaxCallDepth)
	}
}

func TestJSONRPCRequestRoundTrip(t *testing.T) {
	req := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      42,
		Method:  mcpMethodToolsList,
		Params:  nil,
	}
	b, _ := json.Marshal(req)
	var got JSONRPCRequest
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Method != mcpMethodToolsList {
		t.Errorf("unexpected method: %q", got.Method)
	}
	// ID is interface{} so JSON decodes numbers as float64.
	if idF, ok := got.ID.(float64); !ok || idF != 42 {
		t.Errorf("unexpected ID: %v (%T)", got.ID, got.ID)
	}
}

func TestToolRoundTrip(t *testing.T) {
	tool := Tool{
		Name:        "list_dir",
		Description: "List directory contents",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}
	b, _ := json.Marshal(tool)
	var got Tool
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Name != "list_dir" {
		t.Errorf("name mismatch: %q", got.Name)
	}
}

func TestToolCallResultIsError(t *testing.T) {
	ok := ToolCallResult{IsError: false}
	if ok.IsError {
		t.Error("expected IsError false")
	}
	errResult := ToolCallResult{
		IsError: true,
		Content: []ContentBlock{{Type: "text", Text: "something failed"}},
	}
	if !errResult.IsError {
		t.Error("expected IsError true")
	}
	if errResult.Content[0].Text != "something failed" {
		t.Errorf("unexpected content: %q", errResult.Content[0].Text)
	}
}
