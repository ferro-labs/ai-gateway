package httpserver

import (
	"strings"
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
)

// An MCP failure reason is upstream text: it can quote the authorization header
// the handshake sent, the URL it was sent to, or the command line of a
// subprocess. /admin/health is authenticated, which is why it may carry the
// reason at all — but "authenticated" is not "entitled to a credential", and a
// read-only dashboard session is the weakest thing holding one.
//
// Redaction therefore happens here, at the HTTP sink, and not at the registry
// that records the error: the registry's copy is what reaches the server log,
// where the raw text is still wanted.
func TestMCPHealth_RedactsCredentialsAFailureReasonCanQuote(t *testing.T) {
	tests := []struct {
		name    string
		reason  string
		leaked  string
		wantSub string
	}{
		{
			name:    "authorization header",
			reason:  `mcp init search: 401 from https://mcp.example.com/mcp (sent Authorization: Bearer sk-live-4f9c2ab7d1e5)`,
			leaked:  "sk-live-4f9c2ab7d1e5",
			wantSub: "[REDACTED_BEARER_TOKEN]",
		},
		//nolint:gosec // G101: a fake password in a URL is the input under test — the assertion is that it never reaches the response
		{
			name:    "credential in the server URL",
			reason:  `mcp init search: Post "https://ops:hunter2brownfox@mcp.example.com/mcp": connection refused`,
			leaked:  "hunter2brownfox",
			wantSub: "REDACTED",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mcpHealth(aigateway.Readiness{
				MCPServers: []aigateway.MCPServerReadiness{
					{Name: "search", Ready: false, Required: true, LastError: tt.reason},
				},
			})
			if len(got) != 1 {
				t.Fatalf("mcpHealth = %+v, want one entry", got)
			}
			if strings.Contains(got[0].LastError, tt.leaked) {
				t.Errorf("last_error = %q, still carries %q", got[0].LastError, tt.leaked)
			}
			if !strings.Contains(got[0].LastError, tt.wantSub) {
				t.Errorf("last_error = %q, want it to contain %q", got[0].LastError, tt.wantSub)
			}
			// The rest of the reason has to survive: a message redacted down to
			// nothing diagnoses no better than the empty string /readyz serves.
			if !strings.Contains(got[0].LastError, "mcp init search") {
				t.Errorf("last_error = %q, want the diagnostic text kept", got[0].LastError)
			}
		})
	}
}

// A ready server has no reason to carry, and a gateway with no MCP servers
// configured yields no list at all — the caller then omits the key rather than
// publishing an empty one.
func TestMCPHealth_EmptyWhenNoServersAreConfigured(t *testing.T) {
	if got := mcpHealth(aigateway.Readiness{}); got != nil {
		t.Errorf("mcpHealth = %+v, want nil", got)
	}
	got := mcpHealth(aigateway.Readiness{
		MCPServers: []aigateway.MCPServerReadiness{{Name: "filesystem", Ready: true}},
	})
	if len(got) != 1 || got[0].LastError != "" {
		t.Errorf("mcpHealth = %+v, want one entry carrying no reason", got)
	}
}
