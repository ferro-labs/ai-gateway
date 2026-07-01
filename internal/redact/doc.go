// Package redact strips sensitive substrings from text before it is emitted
// to logs or observability backends.
//
// DefaultRedactor applies DefaultPolicies (see policies.go), whose built-in
// rules cover email addresses, bearer tokens, JWTs, AWS access key IDs, and
// provider API keys: Anthropic (sk-ant-), OpenAI legacy (sk-) and modern
// (sk-proj-/sk-svcacct-/sk-admin-), gateway (fgw_), Groq (gsk_), and
// Google/Gemini (AIza). More-specific key patterns are ordered before broader
// ones so a single secret yields exactly one redaction token.
//
// Consumers:
//   - internal/plugins/logger redacts upstream error text before persisting
//     request logs.
//   - internal/otel redacts span and event error messages according to the
//     configured privacy level.
//
// The observability.Provider seam documents this policy in its SetError
// contract; concrete providers (internal/otel) apply it.
package redact
