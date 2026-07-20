package aigateway

import (
	"testing"

	"github.com/ferro-labs/ai-gateway/mcp"
)

// B5: the gateway's environment is deliberately not inherited by MCP
// subprocesses, so ServerConfig.Env is the only channel by which a credential
// can reach one. Before this fix ${VAR} was resolved for Headers only, so a
// subprocess received the literal reference and there was no working way to
// pass a secret at all — which is what config.example.yaml's BRAVE_API_KEY
// entry depended on.
func TestResolveMCPServerRefs_ResolvesEnvAndHeaders(t *testing.T) {
	t.Setenv("FERRO_TEST_MCP_SECRET", "s3cret-value")
	t.Setenv("FERRO_TEST_MCP_TOKEN", "tok-123")

	in := mcp.ServerConfig{
		Name:    "srv",
		Command: "true",
		Env:     map[string]string{"API_KEY": "${FERRO_TEST_MCP_SECRET}"},
		Headers: map[string]string{"Authorization": "Bearer ${FERRO_TEST_MCP_TOKEN}"},
	}

	got, err := resolveMCPServerRefs(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Env["API_KEY"] != "s3cret-value" {
		t.Errorf("Env[API_KEY] = %q, want %q", got.Env["API_KEY"], "s3cret-value")
	}
	if got.Headers["Authorization"] != "Bearer tok-123" {
		t.Errorf("Headers[Authorization] = %q, want %q", got.Headers["Authorization"], "Bearer tok-123")
	}
}

// The caller's Config must keep the reference, never the resolved secret —
// otherwise the secret reaches the config-history store and GET /admin/config.
func TestResolveMCPServerRefs_DoesNotMutateInput(t *testing.T) {
	t.Setenv("FERRO_TEST_MCP_SECRET", "s3cret-value")

	env := map[string]string{"API_KEY": "${FERRO_TEST_MCP_SECRET}"}
	in := mcp.ServerConfig{Name: "srv", Command: "true", Env: env}

	if _, err := resolveMCPServerRefs(in); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if env["API_KEY"] != "${FERRO_TEST_MCP_SECRET}" {
		t.Errorf("input Env was mutated to %q; the resolved secret must not leak back into Config", env["API_KEY"])
	}
}

// An unresolvable reference in Env must fail the server the same way an
// unresolvable header does, rather than silently starting a subprocess with a
// broken credential.
func TestResolveMCPServerRefs_UndefinedEnvVarIsAnError(t *testing.T) {
	requireUnsetEnv(t, mcpUndefinedVar)

	in := mcp.ServerConfig{
		Name:    "srv",
		Command: "true",
		Env:     map[string]string{"API_KEY": "${" + mcpUndefinedVar + "}"},
	}

	if _, err := resolveMCPServerRefs(in); err == nil {
		t.Fatal("expected an error for an undefined ${VAR} in Env, got nil")
	}
}
