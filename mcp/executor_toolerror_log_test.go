package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/pkg/logger"
)

// failingClient answers every tool call with a transport-level failure naming
// internal infrastructure — the detail the model must not be told.
type failingClient struct{ err error }

func (failingClient) Initialize(context.Context) (*ServerInfo, error) { return &ServerInfo{}, nil }
func (failingClient) ListTools(context.Context) ([]Tool, error)       { return nil, nil }
func (c failingClient) CallTool(context.Context, string, json.RawMessage) (*ToolCallResult, error) {
	return nil, c.err
}
func (failingClient) Close() error { return nil }

// A failed tool call must leave an account of itself where the operator can
// read it.
//
// The client and the model receive a fixed generic string, which is right —
// the raw error names hosts, ports and command lines. But the two other places
// that string pointed at are conditional: the span exists only when tracing is
// configured, and the audit hook is a Go-API field with no configuration-file
// equivalent. With neither in place, a failing tool produced no diagnosis
// anywhere at all.
func TestFailedToolCallIsLogged(t *testing.T) {
	internal := "dial tcp 10.4.2.7:8931: connect: connection refused"

	reg := registryWith(map[string]mcpClient{"srv-dead": failingClient{err: errors.New(internal)}})
	reg.mu.Lock()
	reg.servers["srv-dead"].tools = []Tool{{Name: "tool_dead"}}
	reg.toolMap["tool_dead"] = "srv-dead"
	reg.mu.Unlock()

	var buf bytes.Buffer
	old := logger.Default()
	logger.SetDefault(logger.New(logger.Options{Level: "info", Output: &buf}))
	t.Cleanup(func() { logger.SetDefault(old) })

	msg := NewExecutor(reg, 5, nil).executeToolCall(context.Background(), toolCallNamed("tool_dead"))

	// The client still learns nothing beyond the fixed string.
	if strings.Contains(msg.Content, "10.4.2.7") {
		t.Fatalf("internal detail reached the model: %s", msg.Content)
	}

	logged := buf.String()
	if !strings.Contains(logged, "mcp tool call failed") {
		t.Fatalf("no log line for a failed tool call: %s", logged)
	}
	for _, want := range []string{internal, "srv-dead", "tool_dead"} {
		if !strings.Contains(logged, want) {
			t.Errorf("log line does not name %q: %s", want, logged)
		}
	}
}
