// Package observability is the public, semver-stable surface for the
// Ferro Labs AI Gateway observability subsystem.
//
// The package defines the Provider, Span, Exporter, and Event types
// that gateway plugins compile against. Attribute keys and schema rules
// follow the ferro.observability.v1 contract.
//
// The default Provider is NoOp (zero allocations on every method).
// The OpenTelemetry-backed implementation lives in internal/otel and
// is wired in by the gateway at startup when configured.
//
// Plugins in the ai-gateway-plugins repository implement Exporter and
// register themselves via init() → RegisterExporter. Plugins MUST NOT
// import internal/otel; this package is the only allowed import.
package observability
