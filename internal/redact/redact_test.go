package redact

import (
	"regexp"
	"strings"
	"testing"
)

// awsKeyFixture returns a synthetic AWS-access-key-shaped string built
// at runtime so the literal never appears in source files (which would
// trip credential-scanning pre-commit hooks).
func awsKeyFixture(tail string) string {
	pad := 16 - len(tail)
	if pad < 0 {
		pad = 0
	}
	return "AKIA" + strings.Repeat("A", pad) + strings.ToUpper(tail)
}

func TestDefaultRedactorEmail(t *testing.T) {
	r := DefaultRedactor()
	got := r.Redact("contact me at jane.doe@example.com please")
	want := "contact me at [REDACTED_EMAIL] please"
	if got != want {
		t.Errorf("Redact email\n got: %q\nwant: %q", got, want)
	}
}

func TestDefaultRedactorJWT(t *testing.T) {
	r := DefaultRedactor()
	// Valid-shape JWT (header.payload.signature, base64url chars).
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjMifQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"
	got := r.Redact("token=" + jwt + " end")
	want := "token=[REDACTED_JWT] end"
	if got != want {
		t.Errorf("Redact JWT\n got: %q\nwant: %q", got, want)
	}
}

func TestDefaultRedactorBearerToken(t *testing.T) {
	r := DefaultRedactor()
	got := r.Redact("Authorization: Bearer bedrock-token_123.abc/def")
	want := "Authorization: Bearer [REDACTED_BEARER_TOKEN]"
	if got != want {
		t.Errorf("Redact bearer token\n got: %q\nwant: %q", got, want)
	}
}

func TestDefaultRedactorBearerJWTUsesBearerPolicy(t *testing.T) {
	r := DefaultRedactor()
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjMifQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"
	got := r.Redact("Authorization: Bearer " + jwt)
	want := "Authorization: Bearer [REDACTED_BEARER_TOKEN]"
	if got != want {
		t.Errorf("Redact bearer JWT\n got: %q\nwant: %q", got, want)
	}
}

func TestDefaultRedactorAWSKey(t *testing.T) {
	r := DefaultRedactor()
	in := "key=" + awsKeyFixture("example") + " found"
	got := r.Redact(in)
	want := "key=[REDACTED_AWS_KEY] found"
	if got != want {
		t.Errorf("Redact AWS key\n got: %q\nwant: %q", got, want)
	}
}

func TestDefaultRedactorMultiplePatterns(t *testing.T) {
	r := DefaultRedactor()
	in := "user a@b.co with key " + awsKeyFixture("abcd")
	got := r.Redact(in)
	want := "user [REDACTED_EMAIL] with key [REDACTED_AWS_KEY]"
	if got != want {
		t.Errorf("multi-pattern\n got: %q\nwant: %q", got, want)
	}
}

func TestDefaultRedactorLeavesCleanTextAlone(t *testing.T) {
	r := DefaultRedactor()
	in := "no sensitive data here"
	if got := r.Redact(in); got != in {
		t.Errorf("clean text mutated\n got: %q\nwant: %q", got, in)
	}
}

func TestNilRedactorIsSafe(t *testing.T) {
	var r *Redactor
	if got := r.Redact("anything"); got != "anything" {
		t.Errorf("expected pass-through, got %q", got)
	}
	if got := r.Policies(); got != nil {
		t.Errorf("expected nil policies, got %v", got)
	}
}

func TestCustomPolicy(t *testing.T) {
	r := New(Policy{
		Name:        "ssn",
		Pattern:     regexp.MustCompile(`\d{3}-\d{2}-\d{4}`),
		Replacement: "[REDACTED_SSN]",
	})
	got := r.Redact("SSN 123-45-6789 on file")
	want := "SSN [REDACTED_SSN] on file"
	if got != want {
		t.Errorf("custom policy\n got: %q\nwant: %q", got, want)
	}
}

func TestPoliciesReturnsConfiguredList(t *testing.T) {
	r := DefaultRedactor()
	got := r.Policies()
	if len(got) != 10 {
		t.Fatalf("expected 10 default policies, got %d", len(got))
	}
	wantNames := map[string]bool{
		"email": true, "bearer_token": true, "jwt": true, "aws_access_key": true,
		"anthropic_key": true, "openai_modern_key": true, "openai_key": true,
		"gateway_key": true, "groq_key": true, "google_key": true,
	}
	seen := make(map[string]bool, len(got))
	for _, p := range got {
		if !wantNames[p.Name] {
			t.Errorf("unexpected policy name %q", p.Name)
		}
		seen[p.Name] = true
	}
	for name := range wantNames {
		if !seen[name] {
			t.Errorf("missing expected policy %q", name)
		}
	}
}

func TestAwsKeyFixtureMatchesPattern(t *testing.T) {
	// Sanity: the runtime-built fixture must match the AWS key regex,
	// otherwise the tests above would be vacuously passing.
	fixture := awsKeyFixture("example")
	if !regexp.MustCompile(`AKIA[0-9A-Z]{16}`).MatchString(fixture) {
		t.Fatalf("fixture %q does not match AWS key regex", fixture)
	}
}

// buildKey concatenates prefix and body at runtime to avoid credential-scanning
// false positives on string literals in source files.
func buildKey(prefix, body string) string { return prefix + body }

func TestDefaultRedactorAnthropicKey(t *testing.T) {
	r := DefaultRedactor()
	key := buildKey("sk-ant-api03-", strings.Repeat("b", 30))
	got := r.Redact("upstream error: key=" + key + " unauthorized")
	if strings.Contains(got, key) {
		t.Errorf("Anthropic key was not redacted: got %q", got)
	}
	if !strings.Contains(got, "[REDACTED") {
		t.Errorf("expected REDACTED marker in output: got %q", got)
	}
}

func TestDefaultRedactorOpenAIKey(t *testing.T) {
	r := DefaultRedactor()
	key := buildKey("sk-", strings.Repeat("a", 40))
	got := r.Redact("upstream error: key=" + key + " invalid")
	if strings.Contains(got, key) {
		t.Errorf("OpenAI key was not redacted: got %q", got)
	}
	if !strings.Contains(got, "[REDACTED") {
		t.Errorf("expected REDACTED marker in output: got %q", got)
	}
}

func TestDefaultRedactorGatewayKey(t *testing.T) {
	r := DefaultRedactor()
	// Gateway keys are fgw_ + 64 lowercase hex chars (32 random bytes hex-encoded).
	key := buildKey("fgw_", strings.Repeat("a", 64))
	got := r.Redact("rejected gateway key " + key)
	if strings.Contains(got, key) {
		t.Errorf("gateway key was not redacted: got %q", got)
	}
	if !strings.Contains(got, "[REDACTED") {
		t.Errorf("expected REDACTED marker in output: got %q", got)
	}
}

func TestDefaultRedactorGroqKey(t *testing.T) {
	r := DefaultRedactor()
	key := buildKey("gsk_", strings.Repeat("c", 30))
	got := r.Redact("groq rejected " + key)
	if strings.Contains(got, key) {
		t.Errorf("Groq key was not redacted: got %q", got)
	}
	if !strings.Contains(got, "[REDACTED_GROQ_KEY]") {
		t.Errorf("expected [REDACTED_GROQ_KEY] marker in output: got %q", got)
	}
}

func TestDefaultRedactorGoogleGeminiKey(t *testing.T) {
	r := DefaultRedactor()
	// Google/Gemini API keys are "AIza" followed by exactly 35 alphanumeric/underscore/hyphen chars.
	key := buildKey("AIza", strings.Repeat("B", 35))
	got := r.Redact("gemini rejected " + key)
	if strings.Contains(got, key) {
		t.Errorf("Google/Gemini key was not redacted: got %q", got)
	}
	if !strings.Contains(got, "[REDACTED_GOOGLE_KEY]") {
		t.Errorf("expected [REDACTED_GOOGLE_KEY] marker in output: got %q", got)
	}
}

func TestDefaultRedactorOpenAIKeyDoesNotDoubleRedactAnthropic(t *testing.T) {
	// Verify an Anthropic key produces exactly one redaction token,
	// not two (one from the anthropic_key policy, one from openai_key).
	r := DefaultRedactor()
	key := buildKey("sk-ant-api03-", strings.Repeat("b", 30))
	got := r.Redact(key)
	if strings.Count(got, "[REDACTED") != 1 {
		t.Errorf("expected exactly one REDACTED token, got %q", got)
	}
}

func TestDefaultRedactorNewPoliciesDoNotMangleCleanText(t *testing.T) {
	r := DefaultRedactor()
	// Short prefixes and non-key text should pass through unchanged.
	in := "model=gpt-4 request=chat usage_tokens=123 gsk=short fgw=tiny"
	if got := r.Redact(in); got != in {
		t.Errorf("clean text was mangled: got %q, want %q", got, in)
	}
}

func TestDefaultRedactorOpenAIModernProjectKey(t *testing.T) {
	r := DefaultRedactor()
	// sk-proj- followed by 20+ alphanumeric/underscore/hyphen chars.
	key := buildKey("sk-proj-", strings.Repeat("A", 30))
	got := r.Redact("upstream error: key=" + key + " unauthorized")
	if strings.Contains(got, key) {
		t.Errorf("modern OpenAI project key was not redacted: got %q", got)
	}
	if !strings.Contains(got, "[REDACTED_OPENAI_KEY]") {
		t.Errorf("expected [REDACTED_OPENAI_KEY] marker in output: got %q", got)
	}
}

func TestDefaultRedactorOpenAIModernSvcAcctKey(t *testing.T) {
	r := DefaultRedactor()
	// sk-svcacct- followed by 20+ alphanumeric/underscore/hyphen chars.
	key := buildKey("sk-svcacct-", strings.Repeat("Z", 25))
	got := r.Redact("auth failed with key=" + key)
	if strings.Contains(got, key) {
		t.Errorf("modern OpenAI svcacct key was not redacted: got %q", got)
	}
	if !strings.Contains(got, "[REDACTED_OPENAI_KEY]") {
		t.Errorf("expected [REDACTED_OPENAI_KEY] marker in output: got %q", got)
	}
}

func TestDefaultRedactorOpenAIModernKeyShortNotRedacted(t *testing.T) {
	r := DefaultRedactor()
	// sk-proj- with fewer than 20 body chars must NOT be treated as a key.
	shortKey := "sk-proj-abc"
	in := "info: project=" + shortKey + " loaded"
	got := r.Redact(in)
	if got != in {
		t.Errorf("short sk-proj- token was incorrectly redacted: got %q, want %q", got, in)
	}
}

func TestDefaultRedactorOpenAIModernKeyDoesNotDoubleRedact(t *testing.T) {
	// A modern OpenAI project key must produce exactly one redaction token.
	r := DefaultRedactor()
	key := buildKey("sk-proj-", strings.Repeat("A", 30))
	got := r.Redact(key)
	if strings.Count(got, "[REDACTED") != 1 {
		t.Errorf("expected exactly one REDACTED token for modern key, got %q", got)
	}
}
