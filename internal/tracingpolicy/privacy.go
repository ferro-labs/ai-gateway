// Package tracingpolicy holds tracing-config validation helpers shared by the
// root config validator and the internal/otel config validator. It is not
// part of the public observability seam (Provider/Span/Exporter/Event).
package tracingpolicy

import "fmt"

// Tracing privacy levels control how much prompt/response/error content is
// exported on spans. An empty string is accepted and treated as the default
// (metadata) by the tracing backend.
const (
	PrivacyLevelNone     = "none"
	PrivacyLevelMetadata = "metadata"
	PrivacyLevelFull     = "full"
)

// ValidatePrivacyLevel reports an error when level is not a recognised tracing
// privacy level. An empty string is accepted (treated as the default). This is
// the single source of truth shared by the gateway config validator and the
// tracing config validator.
func ValidatePrivacyLevel(level string) error {
	switch level {
	case "", PrivacyLevelNone, PrivacyLevelMetadata, PrivacyLevelFull:
		return nil
	default:
		return fmt.Errorf("invalid privacy_level %q: must be one of none, metadata, full", level)
	}
}
