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
	}
}
