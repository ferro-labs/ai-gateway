// Package redact strips sensitive substrings from text before it is
// emitted to logs or observability backends.
//
// The package is used by:
//   - internal/logging when emitting structured records
//   - internal/otel when privacy_level is "full" and prompt/response
//     content is included in span attributes
//   - the request-logger plugin when persisting request bodies
//
// Scaffolding revision (v1.1.0-observability branch): ships a minimal
// default policy covering emails, JWTs, and AWS access keys. The full
// policy library (credit cards, phone numbers, configurable rules) is
// expanded in the v1.1.0 implementation PRs.
package redact
