package redact

import (
	"fmt"
	"strings"
	"testing"
)

// Synthetic credentials with the same shape as the real provider formats. Each
// is assembled at runtime so no credential-shaped literal sits in source and
// trips a scanning pre-commit hook.
var (
	mistralShaped    = strings.Repeat("Ka7bQz3Rt", 3) + "Wm9XcV" // 33 alnum, no prefix
	cohereShaped     = strings.Repeat("Zt4mQw8Rb", 4) + "Nc1X"   // 40 alnum, no prefix
	perplexityShaped = "pplx-" + strings.Repeat("4Kd8Qm2Wt6Rb", 4)
	qwenShaped       = "sk-ws-9Fq2.Lt7Xb4.Nz8Vd1Mc6.Rg3Hy5Kp0Ju"
	deepseekShaped   = "sk-" + strings.Repeat("Wq7Zt3Nb", 4)
)

// withSecrets replaces the process-wide secret set for one test and restores it
// afterwards. It reaches past RegisterSecretValues deliberately: the exported
// API is additive, and a test needs to prove behaviour with a *known* set.
func withSecrets(t *testing.T, pairs ...string) {
	t.Helper()
	seed()
	prev := secrets.Load()
	secrets.Store(newValueMatcher(pairs))
	t.Cleanup(func() { secrets.Store(prev) })
}

// TestValueRedaction_PrefixlessKeys is F62: the four credential formats the
// shape-based policies cannot match. Before value redaction each of these
// reached the span, the log, the persisted request log and the exporter event
// verbatim. DeepSeek is the control that the pattern layer already handled.
func TestValueRedaction_PrefixlessKeys(t *testing.T) {
	withSecrets(t,
		mistralShaped, "[REDACTED:MISTRAL_API_KEY]",
		cohereShaped, "[REDACTED:COHERE_API_KEY]",
	)

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "mistral 32 alnum, no prefix",
			in:   "mistral API error (401): Unauthorized: invalid api_key: " + mistralShaped,
			want: "mistral API error (401): Unauthorized: invalid api_key: [REDACTED:MISTRAL_API_KEY]",
		},
		{
			name: "cohere 40 alnum, no prefix",
			in:   `cohere API error (401): {"message":"invalid api_key: ` + cohereShaped + `"}`,
			want: `cohere API error (401): {"message":"invalid api_key: [REDACTED:COHERE_API_KEY]"}`,
		},
		{
			name: "fallback concatenates several providers' keys into one string",
			in:   "all providers failed: mistral: " + mistralShaped + "; cohere: " + cohereShaped,
			want: "all providers failed: mistral: [REDACTED:MISTRAL_API_KEY]; cohere: [REDACTED:COHERE_API_KEY]",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := String(tc.in); got != tc.want {
				t.Fatalf("String()\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// TestPatternBackstop_PrefixedKeys covers the credentials the gateway may never
// have seen — a key echoed by an upstream the operator never configured — which
// only the shape rules can catch.
func TestPatternBackstop_PrefixedKeys(t *testing.T) {
	withSecrets(t) // no registered values: patterns alone

	cases := []struct{ name, in, want string }{
		{"perplexity", "invalid api_key: " + perplexityShaped, "invalid api_key: [REDACTED_PERPLEXITY_KEY]"},
		{"qwen dot-separated", "invalid api_key: " + qwenShaped, "invalid api_key: [REDACTED_QWEN_KEY]"},
		{"deepseek control", "invalid api_key: " + deepseekShaped, "invalid api_key: [REDACTED_OPENAI_KEY]"},
		{
			"keyword-anchored prefix-less key",
			"upstream said api_key=" + mistralShaped,
			"upstream said api_key=[REDACTED_CREDENTIAL]",
		},
		{
			"pem private key",
			"vertex: -----BEGIN RSA PRIVATE KEY-----\nMIIabc\n-----END RSA PRIVATE KEY----- rejected",
			"vertex: [REDACTED_PRIVATE_KEY] rejected",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := String(tc.in); got != tc.want {
				t.Fatalf("String()\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// TestKeywordSecret_DoesNotEatOrdinaryText guards the one rule with a plausible
// false-positive surface. A bare 32-character token is a digest, a trace ID or a
// git SHA far more often than a credential, so keyword_secret must fire only on
// an assignment to a credential-named key.
func TestKeywordSecret_DoesNotEatOrdinaryText(t *testing.T) {
	withSecrets(t)

	unchanged := []string{
		`{"trace_id":"395c0e3b543927aa84b338a11392ce9b","status":500}`,
		"commit 9f2a1c4e8b7d6a5f3e2c1b0a9d8e7f6a5b4c3d2e failed to build",
		"checksum e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		`{"model":"gpt-4o-mini","tokens_in":24,"tokens_out":128}`,
		"api_key: [REDACTED_OPENAI_KEY]",
	}
	for _, in := range unchanged {
		if got := String(in); got != in {
			t.Errorf("String(%q) = %q, want it unchanged", in, got)
		}
	}
}

func TestIsCredentialName(t *testing.T) {
	credential := []string{
		"OPENAI_API_KEY", "MISTRAL_API_KEY", "COHERE_API_KEY", "AWS_SECRET_ACCESS_KEY",
		"AWS_ACCESS_KEY_ID", "AWS_SESSION_TOKEN", "AWS_BEARER_TOKEN_BEDROCK",
		"DATABRICKS_TOKEN", "REPLICATE_API_TOKEN", "MASTER_KEY", "API_KEY_STORE_DSN",
		"CONFIG_STORE_DSN", "REQUEST_LOG_STORE_DSN", "DB_PASSWORD",
		// The same judgement serves config-map keys and HTTP header names, which
		// is why the split is on components rather than on the underscore alone.
		"api_key", "auth_token", "client_secret", "Authorization",
		"Proxy-Authorization", "x-api-key", "X-Goog-Api-Key",
	}
	for _, name := range credential {
		if !IsCredentialName(name) {
			t.Errorf("IsCredentialName(%q) = false, want true", name)
		}
	}

	// Component matching, not substring: these hold no credential and must not
	// have their values blanked out of every log line, nor be withheld from an
	// operator reading their own configuration.
	ordinary := []string{
		"OLLAMA_HOST", "OPENAI_BASE_URL", "AZURE_OPENAI_ENDPOINT", "AWS_REGION",
		"OLLAMA_MODELS", "FERRO_OLLAMA_MODELS", "AZURE_OPENAI_DEPLOYMENT", "CLOUDFLARE_ACCOUNT_ID",
		"VERTEX_AI_PROJECT_ID", "PATH", "HOME", "XAUTHORITY", "SSH_AUTH_SOCK",
		"MONKEYPATCH", "GATEWAY_CONFIG", "PORT",
		"level", "persist", "store_id", "blocked_words", "case_sensitive",
		"spend_limit_usd", "ttl_seconds", "requests_per_second", "Content-Type",
	}
	for _, name := range ordinary {
		if IsCredentialName(name) {
			t.Errorf("IsCredentialName(%q) = true, want false", name)
		}
	}
}

// TestValueFloor is the guard against a value-based redactor eating ordinary
// words. A misconfigured OPENAI_API_KEY=test must not delete "test" from every
// log line the gateway writes.
func TestValueFloor(t *testing.T) {
	rejected := []string{
		"", "test", "changeme", "secret", "none", "localhost",
		"eleven-chr", // 10, under the floor
		"${OPENAI_API_KEY}",
		"true", "false", "TRUE",
		"a value with spaces in it",
	}
	for _, v := range rejected {
		if isRedactableValue(v) {
			t.Errorf("isRedactableValue(%q) = true, want false", v)
		}
	}

	accepted := []string{mistralShaped, cohereShaped, perplexityShaped, qwenShaped, deepseekShaped}
	for _, v := range accepted {
		if !isRedactableValue(v) {
			t.Errorf("isRedactableValue(%q) = false, want true", v)
		}
	}
}

func TestRegisterSecretValues_IgnoresDegenerateValues(t *testing.T) {
	withSecrets(t)
	RegisterSecretValues("test", "abc", mistralShaped)

	if got := Values("a test of abc"); got != "a test of abc" {
		t.Fatalf("short values must not be registered, got %q", got)
	}
	if got := Values("key " + mistralShaped); got != "key "+genericSecretToken {
		t.Fatalf("Values() = %q, want the registered value replaced", got)
	}
}

// TestValues_AllocationFreeWhenClean pins the hot-path property the logging
// sink depends on: a line carrying no secret must not allocate.
func TestValues_AllocationFreeWhenClean(t *testing.T) {
	withSecrets(t, mistralShaped, "[REDACTED:MISTRAL_API_KEY]", cohereShaped, "[REDACTED:COHERE_API_KEY]")
	line := `{"level":"INFO","msg":"gateway request","model":"gpt-4o-mini","latency_ms":42}`

	if n := testing.AllocsPerRun(200, func() { _ = Values(line) }); n != 0 {
		t.Fatalf("Values() allocated %.1f times on a clean line, want 0", n)
	}
}

func BenchmarkValues(b *testing.B) {
	line := `{"time":"2026-07-28T13:32:36Z","level":"ERROR","msg":"gateway error",` +
		`"trace_id":"395c0e3b543927aa84b338a11392ce9b","model":"codestral-2405",` +
		`"error":"all providers failed: provider mistral attempt 1: mistral API error (401)"}`

	build := func(n int) []string {
		pairs := make([]string, 0, n*2)
		for i := range n {
			pairs = append(pairs, string(rune('A'+i))+mistralShaped, "[REDACTED:K]")
		}
		return pairs
	}

	for _, n := range []int{1, 5, 10, 30} {
		m := newValueMatcher(build(n))
		b.Run(fmt.Sprintf("clean/secrets=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = m.redact(line)
			}
		})
	}

	m := newValueMatcher(build(10))
	hit := line + " key=" + string(rune('A')) + mistralShaped
	b.Run("hit/secrets=10", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = m.redact(hit)
		}
	})
}

func BenchmarkStringPatterns(b *testing.B) {
	line := `gateway error: all providers failed: provider mistral attempt 1: mistral API error (401)`
	b.ReportAllocs()
	for b.Loop() {
		_ = String(line)
	}
}
