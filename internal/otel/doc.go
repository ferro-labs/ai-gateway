// Package otel wires the gateway core to OpenTelemetry.
//
// This is the ONLY internal package permitted to import the
// OpenTelemetry SDK. Everywhere else in the gateway depends on the
// public observability package, which exposes the OTel-independent
// Provider, Span, and Exporter interfaces.
//
// Scaffolding revision (v1.1.0-observability branch): Init always
// returns observability.NoOp. Subsequent PRs add the actual OTel SDK
// initialisation, OTLP exporters, slog handler bridge, and HTTP
// middleware.
//
// Callers MUST always invoke the ShutdownFunc returned by Init from
// the gateway's graceful-shutdown sequence.
package otel
