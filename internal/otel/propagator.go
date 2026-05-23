package otel

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// installPropagator sets the global TextMapPropagator to the composite
// W3C TraceContext + Baggage propagator. This matches the OTel SDK
// default and is required so inbound traceparent headers attach to
// gateway spans and outbound HTTP calls re-emit them.
//
// Idempotent: safe to call multiple times.
func installPropagator() {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
}
