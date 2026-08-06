package redact

import "regexp"

// DefaultPolicies returns the redaction rules applied when no custom
// policy set is supplied.
//
// Coverage:
//   - email addresses
//   - bearer tokens
//   - JWTs (header.payload.signature)
//   - AWS access key IDs (AKIA…)
//   - Anthropic API keys (sk-ant-…)         — matched before the openai_key rules
//   - OpenAI modern keys (sk-proj-…, etc.)  — hyphenated project/service-account keys
//   - OpenAI API keys (sk-…)                — legacy 48-char alphanumeric keys
//   - Gateway API keys (fgw_…)              — hex-encoded 32-byte tokens issued by ferrogw
//   - Groq API keys (gsk_…)
//   - Google/Gemini API keys (AIza…)
//   - Qwen workspace keys (sk-ws-…)         — dot-separated, matched before openai_key
//   - Perplexity API keys (pplx-…)
//   - credential-named URL query parameters (?access_token=…)
//   - URL userinfo passwords (postgres://user:pw@host)
//   - PEM private key blocks
//   - keyword-anchored generic secrets      — last resort, see keyword_secret
//
// These are a *backstop*. The primary mechanism is value redaction (values.go):
// every credential the gateway itself holds is removed by exact value before any
// policy runs, which is the only thing that works for the prefix-less keys
// Mistral, Cohere, Together, Fireworks and DeepInfra issue.
//
// Coverage planned for a future release:
//   - credit card numbers (Luhn-validated)
//   - phone numbers (E.164 + common national formats)
//   - operator-supplied custom regex policies
//
// Policy ordering: more-specific patterns (anthropic_key, openai_modern_key)
// are placed before less-specific ones (openai_key) so that a single key
// produces exactly one redaction token rather than two.
func DefaultPolicies() []Policy {
	return []Policy{
		{
			Name:        "email",
			Pattern:     regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`),
			Replacement: "[REDACTED_EMAIL]",
		},
		{
			Name:        "bearer_token",
			Pattern:     regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/\-]+=*`),
			Replacement: "Bearer [REDACTED_BEARER_TOKEN]",
		},
		{
			Name:        "jwt",
			Pattern:     regexp.MustCompile(`eyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+`),
			Replacement: "[REDACTED_JWT]",
		},
		{
			Name:        "aws_access_key",
			Pattern:     regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
			Replacement: "[REDACTED_AWS_KEY]",
		},
		// anthropic_key must precede both openai_modern_key and openai_key:
		// Anthropic keys begin with "sk-ant-" which overlaps the broader "sk-"
		// space. The most-specific prefix is matched first.
		{
			Name: "anthropic_key",
			// sk-ant- followed by 20+ alphanumeric, underscore, or hyphen chars.
			Pattern:     regexp.MustCompile(`sk-ant-[A-Za-z0-9_\-]{20,}`),
			Replacement: "[REDACTED_ANTHROPIC_KEY]",
		},
		// openai_modern_key covers hyphenated project/service-account/admin keys
		// (sk-proj-*, sk-svcacct-*, sk-admin-*) introduced in 2024. These contain
		// hyphens inside the body, so the legacy pure-alphanumeric openai_key rule
		// would stop at the first embedded hyphen and fail to match them fully.
		// This rule is placed after anthropic_key (which also starts "sk-") so
		// that "sk-ant-..." is consumed first.
		{
			Name: "openai_modern_key",
			Pattern: regexp.MustCompile(
				`sk-(proj|svcacct|admin)-[A-Za-z0-9_\-]{20,}`,
			),
			Replacement: "[REDACTED_OPENAI_KEY]",
		},
		{
			Name: "openai_key",
			// sk- followed by 20+ pure alphanumeric chars (legacy 48-char format).
			// The pure-alphanumeric class stops at any hyphen, so "sk-ant-..." and
			// modern "sk-proj-..." keys are not re-matched here after the more
			// specific rules above have already fired.
			Pattern:     regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`),
			Replacement: "[REDACTED_OPENAI_KEY]",
		},
		// qwen_key must precede openai_key. A Qwen workspace key is
		// "sk-ws-" followed by dot-separated segments; the pure-alphanumeric
		// openai_key class stops at the first "." and would leave the rest of
		// the key in the text.
		{
			Name:        "qwen_key",
			Pattern:     regexp.MustCompile(`sk-ws-[A-Za-z0-9][A-Za-z0-9._\-]{14,}`),
			Replacement: "[REDACTED_QWEN_KEY]",
		},
		{
			Name: "perplexity_key",
			// pplx- followed by 20+ alphanumeric chars (Perplexity issues 48).
			Pattern:     regexp.MustCompile(`pplx-[A-Za-z0-9]{20,}`),
			Replacement: "[REDACTED_PERPLEXITY_KEY]",
		},
		{
			Name: "gateway_key",
			// fgw_ followed by 32+ lowercase hex chars.
			// ferrogw issues keys as "fgw_" + hex.EncodeToString(32 random bytes),
			// producing exactly 64 hex characters; {32,} also covers shorter test fixtures.
			Pattern:     regexp.MustCompile(`fgw_[0-9a-f]{32,}`),
			Replacement: "[REDACTED_GW_KEY]",
		},
		{
			Name: "groq_key",
			// gsk_ followed by 20+ alphanumeric chars (Groq API key format).
			Pattern:     regexp.MustCompile(`gsk_[A-Za-z0-9]{20,}`),
			Replacement: "[REDACTED_GROQ_KEY]",
		},
		{
			Name: "google_key",
			// AIza followed by exactly 35 alphanumeric, underscore, or hyphen chars
			// (Google Cloud / Gemini API key format).
			Pattern:     regexp.MustCompile(`AIza[A-Za-z0-9_\-]{35}`),
			Replacement: "[REDACTED_GOOGLE_KEY]",
		},
		{
			Name:        "url_credential",
			Pattern:     urlCredentialParam,
			Replacement: "${1}" + redactedURLValue,
		},
		{
			Name:        "url_userinfo",
			Pattern:     urlUserinfoPassword,
			Replacement: "${1}" + redactedURLValue + "@",
		},
		{
			Name: "private_key_block",
			// A PEM private key, however it is labelled (RSA, EC, OPENSSH…).
			// Vertex AI service-account JSON carries one, and a JSON-encoded
			// blob reaches a log with its newlines escaped, so both forms match.
			Pattern:     regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`),
			Replacement: "[REDACTED_PRIVATE_KEY]",
		},
		// keyword_secret is the last resort, and deliberately the weakest rule.
		//
		// Several providers issue keys with no prefix at all — Mistral is 32
		// alphanumerics, Cohere 40 — so there is no shape to match on. A bare
		// `[A-Za-z0-9]{32}` would also match every MD5 digest, git SHA,
		// dash-less UUID, trace ID and request ID in the logs, which is why no
		// secret scanner ships one: gitleaks' Cohere rule captures a bare
		// ([a-zA-Z0-9]{40}) and relies entirely on the literal word "cohere"
		// nearby, and TruffleHog anchors on a keyword within 40 characters for
		// the same reason. This rule follows that precedent: an assignment to a
		// credential-named key, then 24+ alphanumerics.
		//
		// It is a backstop only. The gateway's own credentials are removed by
		// value before any policy runs, which needs no shape and no keyword.
		{
			Name: "keyword_secret",
			Pattern: regexp.MustCompile(
				`(?i)((?:api[_\-]?key|apikey|access[_\-]?token|auth[_\-]?token|token|secret|password)["']?\s*[:=]\s*["']?)([A-Za-z0-9]{24,64})\b`,
			),
			Replacement: "${1}[REDACTED_CREDENTIAL]",
		},
	}
}
