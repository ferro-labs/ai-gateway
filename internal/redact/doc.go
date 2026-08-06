// Package redact strips credentials from text before it is emitted to logs,
// spans, stored request logs, observability exporters or HTTP responses.
//
// # Two layers, in this order
//
// Value redaction (values.go) is primary. The gateway holds its own
// credentials, so it does not have to guess their shape: every configured
// credential value is replaced wherever it appears, by exact match. This is the
// only mechanism that works for the prefix-less keys Mistral (32 alphanumerics)
// and Cohere (40) issue, where no regex can distinguish a key from a digest.
// The set is derived from the process environment, which is where a self-hosted
// gateway's credentials already live; an embedder injecting credentials
// programmatically calls RegisterSecretValues.
//
// Pattern redaction (policies.go) is the backstop, for a credential the gateway
// never saw. DefaultPolicies covers email addresses, bearer tokens, JWTs, AWS
// access key IDs, PEM private key blocks, and the prefixed provider formats:
// Anthropic (sk-ant-), OpenAI legacy (sk-) and modern
// (sk-proj-/sk-svcacct-/sk-admin-), Qwen (sk-ws-), Perplexity (pplx-), gateway
// (fgw_), Groq (gsk_) and Google/Gemini (AIza). More-specific key patterns are
// ordered before broader ones so a single secret yields exactly one token.
//
// # Consumers
//
//   - pkg/logger applies the value layer to every emitted log line, so a raw
//     error logged without an explicit redaction call still cannot carry a
//     configured credential to stdout.
//   - internal/otel redacts span status and exception messages according to the
//     configured privacy level.
//   - internal/requestlog redacts at the store writer, so every persisted row
//     is covered regardless of which component wrote it.
//   - internal/events redacts the failed-request payload every observability
//     exporter receives.
//   - internal/apierror redacts client-facing error bodies.
//
// The observability.Provider seam documents this policy in its SetError
// contract; concrete providers (internal/otel) apply it.
//
// # Not here: which configuration keys GET /admin/config serves
//
// This package judges *text* and *values*. It deliberately does not decide
// which keys of an operator-supplied configuration map are safe to serve over
// the admin API, and a name-matching predicate for that should not be added
// back. Serving is an allowlist question — a key is shown when the component
// receiving it declares it a setting, and withheld otherwise — so an
// unrecognised name is withheld rather than served. A vocabulary of
// credential-ish name fragments answers it the wrong way round and cannot be
// completed: pat, sid, jwt, hmac, otp, dsn and every non-English spelling name
// a credential and match no fragment.
//
// That rule lives in internal/admin/repository (shownKeys, scrub.go), next to
// the walk that applies it. IsCredentialName stays here because it answers a
// different question with the opposite cost: which environment values to
// enrol for redaction everywhere the gateway writes text, where over-matching
// deletes ordinary words from every log line.
package redact
