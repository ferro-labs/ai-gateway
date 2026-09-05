package otel

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/observability"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestProvider_StartAttemptSpan_NestsUnderRequestSpan(t *testing.T) {
	prov, exp := newTestProvider(t)

	ctx, root := prov.StartRequestSpan(context.Background(), observability.RequestAttrs{Operation: "chat", RequestModel: "gpt-4o"})
	_, attempt := prov.StartAttemptSpan(ctx, "openai-prod", 2)
	attempt.SetAttribute(observability.AttrFerroRoutingOutcome, string(observability.RoutingAttemptError))
	attempt.SetError(errors.New("dial account@example.com refused"))
	attempt.End()
	root.End()

	spans := exp.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("spans = %d, want root + attempt", len(spans))
	}
	var attemptSpan, rootSpan tracetest.SpanStub
	for _, s := range spans {
		switch s.Name {
		case observability.SpanNameRoutingAttempt:
			attemptSpan = s
		case "gateway.request":
			rootSpan = s
		}
	}
	if attemptSpan.Name == "" {
		t.Fatalf("no %s span exported", observability.SpanNameRoutingAttempt)
	}
	if attemptSpan.Parent.SpanID() != rootSpan.SpanContext.SpanID() {
		t.Errorf("attempt parent = %s, want the request span %s", attemptSpan.Parent.SpanID(), rootSpan.SpanContext.SpanID())
	}
	if attemptSpan.SpanKind != trace.SpanKindClient {
		t.Errorf("attempt kind = %v, want CLIENT", attemptSpan.SpanKind)
	}
	assertAttrs(t, attemptSpan.Attributes, map[string]attribute.Value{
		observability.AttrFerroRoutingTargetKey: attribute.StringValue("openai-prod"),
		observability.AttrFerroRoutingSequence:  attribute.IntValue(2),
		observability.AttrFerroRoutingOutcome:   attribute.StringValue("error"),
	})
	if attemptSpan.Status.Code != codes.Error {
		t.Errorf("attempt status = %v, want Error", attemptSpan.Status.Code)
	}
	for _, ev := range attemptSpan.Events {
		for _, kv := range ev.Attributes {
			if strings.Contains(kv.Value.AsString(), "account@example.com") {
				t.Errorf("attempt span leaked the unredacted error: %q", kv.Value.AsString())
			}
		}
	}
	// The status description is the other place the error message lands:
	// recordSpanError sets it alongside the event, from the same text.
	// Checking only the event would miss a leak on this half.
	if strings.Contains(attemptSpan.Status.Description, "account@example.com") {
		t.Errorf("attempt span status leaked the unredacted error: %q", attemptSpan.Status.Description)
	}
}
