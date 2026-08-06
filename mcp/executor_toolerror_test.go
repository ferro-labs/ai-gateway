package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/client/transport"
)

// TestToolErrorContent is F52.
//
// The tool-result message is handed to the LLM provider and, through the
// assistant's reply, to the end caller. It used to carry err.Error(), which for
// a transport failure names the MCP server's host and port or its subprocess
// command line. Redaction cannot help — a hostname has no credential shape — so
// the message is drawn from a closed set instead.
func TestToolErrorContent(t *testing.T) {
	internal := "dial tcp 10.4.2.7:8931: connect: connection refused (mcp-internal.svc.cluster.local)"

	cases := []struct {
		name string
		err  error
		want string
	}{
		{"generic failure", errors.New(internal), "the tool call failed"},
		{"timeout", fmt.Errorf("call %s: %w", internal, context.DeadlineExceeded), "the tool call timed out"},
		{"dead transport", fmt.Errorf("write %s: %w", internal, transport.ErrTransportClosed), "the tool server is unavailable"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := toolErrorContent(tc.err)

			var payload map[string]string
			if err := json.Unmarshal([]byte(got), &payload); err != nil {
				t.Fatalf("tool result is not valid JSON: %v (%s)", err, got)
			}
			if payload["error"] != tc.want {
				t.Fatalf("error = %q, want %q", payload["error"], tc.want)
			}
			for _, leak := range []string{"10.4.2.7", "8931", "cluster.local", "dial tcp"} {
				if strings.Contains(got, leak) {
					t.Fatalf("internal detail %q reached the model: %s", leak, got)
				}
			}
		})
	}
}
