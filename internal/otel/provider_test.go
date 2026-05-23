package otel

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/observability"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// newTestProvider returns an otelProvider whose spans are captured by
// an in-memory exporter, allowing assertions on emitted attributes.
func newTestProvider(t *testing.T) (*otelProvider, *tracetest.InMemoryExporter) {
	t.Helper()
	return newTestProviderWithConfig(t, DefaultConfig())
}

// newTestProviderWithConfig is like newTestProvider but accepts a
// custom Config so tests can control fields such as PrivacyLevel.
func newTestProviderWithConfig(t *testing.T, cfg Config) (*otelProvider, *tracetest.InMemoryExporter) {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exp),
	)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return newProvider(tp, cfg), exp
}

func TestProvider_StartRequestSpan_StampsAttributes(t *testing.T) {
	prov, exp := newTestProvider(t)

	_, span := prov.StartRequestSpan(context.Background(), observability.RequestAttrs{
		System:          "openai",
		Operation:       "chat",
		RequestModel:    "gpt-4o",
		ResponseModel:   "gpt-4o-2024-08-06",
		IsStream:        true,
		RoutingStrategy: "single",
		TargetKey:       "openai-prod",
		TraceID:         "0af7651916cd43dd8448eb211c80319c",
	})
	span.End()

	got := exp.GetSpans()
	if len(got) != 1 {
		t.Fatalf("expected 1 span, got %d", len(got))
	}

	want := map[string]attribute.Value{
		observability.AttrGenAISystem:           attribute.StringValue("openai"),
		observability.AttrGenAIOperationName:    attribute.StringValue("chat"),
		observability.AttrGenAIRequestModel:     attribute.StringValue("gpt-4o"),
		observability.AttrGenAIResponseModel:    attribute.StringValue("gpt-4o-2024-08-06"),
		observability.AttrGenAIRequestIsStream:  attribute.BoolValue(true),
		observability.AttrFerroRoutingStrategy:  attribute.StringValue("single"),
		observability.AttrFerroRoutingTargetKey: attribute.StringValue("openai-prod"),
		observability.AttrFerroGatewayTraceID:   attribute.StringValue("0af7651916cd43dd8448eb211c80319c"),
		observability.AttrFerroSchemaVersion:    attribute.StringValue(observability.SchemaVersion),
	}
	assertAttrs(t, got[0].Attributes, want)
}

func TestSpan_SetTokensAndCost(t *testing.T) {
	prov, exp := newTestProvider(t)

	_, span := prov.StartRequestSpan(context.Background(), observability.RequestAttrs{})
	span.SetTokens(120, 45, 0)
	span.SetCost(observability.CostBreakdown{
		TotalUSD:   0.0042,
		InputUSD:   0.0030,
		OutputUSD:  0.0012,
		ModelFound: true,
	})
	span.End()

	got := exp.GetSpans()[0]
	assertAttrs(t, got.Attributes, map[string]attribute.Value{
		observability.AttrGenAIUsageInputTokens:  attribute.IntValue(120),
		observability.AttrGenAIUsageOutputTokens: attribute.IntValue(45),
		observability.AttrFerroCostUSD:           attribute.Float64Value(0.0042),
		observability.AttrFerroCostInputUSD:      attribute.Float64Value(0.0030),
		observability.AttrFerroCostOutputUSD:     attribute.Float64Value(0.0012),
		observability.AttrFerroCostModelFound:    attribute.BoolValue(true),
	})
}

func TestSpan_SetError_RedactsAndMarksError(t *testing.T) {
	prov, exp := newTestProvider(t)

	_, span := prov.StartRequestSpan(context.Background(), observability.RequestAttrs{})
	span.SetError(errors.New("upstream failed: contact admin@example.com"))
	span.End()

	got := exp.GetSpans()[0]
	if got.Status.Code != codes.Error {
		t.Fatalf("span status code = %v, want Error", got.Status.Code)
	}
	if got.Status.Description == "" || got.Status.Description == "upstream failed: contact admin@example.com" {
		t.Fatalf("expected redacted status, got %q", got.Status.Description)
	}
}

func TestSpan_StartChildHasParent(t *testing.T) {
	prov, exp := newTestProvider(t)

	ctx, root := prov.StartRequestSpan(context.Background(), observability.RequestAttrs{})
	_, child := root.StartChild(ctx, "provider.openai.chat", observability.SpanKindClient)
	child.End()
	root.End()

	spans := exp.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("expected 2 spans, got %d", len(spans))
	}
	// Child finishes first, so spans[0] is the child.
	if !spans[0].Parent.SpanID().IsValid() {
		t.Fatal("expected child span to have a parent")
	}
	if spans[0].SpanContext.TraceID() != spans[1].SpanContext.TraceID() {
		t.Fatal("expected child and root to share a trace id")
	}
}

// TestProvider_StartRequestSpan_RoutingAttrsAbsentWhenEmpty asserts that
// ferro.routing.strategy and ferro.routing.target_key are NOT present on the
// span when the corresponding RequestAttrs fields are empty strings. This
// prevents span attribute pollution with empty values.
func TestProvider_StartRequestSpan_RoutingAttrsAbsentWhenEmpty(t *testing.T) {
	prov, exp := newTestProvider(t)

	_, span := prov.StartRequestSpan(context.Background(), observability.RequestAttrs{
		Operation:    "chat",
		RequestModel: "gpt-4o",
		IsStream:     false,
		// RoutingStrategy and TargetKey intentionally left empty.
	})
	span.End()

	got := exp.GetSpans()
	if len(got) != 1 {
		t.Fatalf("expected 1 span, got %d", len(got))
	}

	gotMap := map[string]attribute.Value{}
	for _, kv := range got[0].Attributes {
		gotMap[string(kv.Key)] = kv.Value
	}

	if _, present := gotMap[observability.AttrFerroRoutingStrategy]; present {
		t.Errorf("ferro.routing.strategy should be ABSENT when RoutingStrategy is empty, got %q",
			gotMap[observability.AttrFerroRoutingStrategy].Emit())
	}
	if _, present := gotMap[observability.AttrFerroRoutingTargetKey]; present {
		t.Errorf("ferro.routing.target_key should be ABSENT when TargetKey is empty, got %q",
			gotMap[observability.AttrFerroRoutingTargetKey].Emit())
	}
}

// TestSpan_SetError_PrivacyLevels verifies the three privacy-level
// semantics for SetError. The test error contains both an email address
// and an AWS access key so we can confirm redaction behaviour across all
// three modes.
func TestSpan_SetError_PrivacyLevels(t *testing.T) {
	// rawErr contains PII that the redactor knows about: an email and an
	// AWS access key ID matching the AKIA[0-9A-Z]{16} pattern.
	rawErr := errors.New("user bob@example.com failed: AKIA1234567890ABCDEF")

	tests := []struct {
		name         string
		privacyLevel string
		// wantStatus is the exact status description expected on the span.
		wantStatus string
		// wantAbsent is a list of substrings that must NOT appear in the
		// status description.
		wantAbsent []string
		// wantPresent is a list of substrings that MUST appear in the
		// status description (empty means no positive assertions beyond
		// wantStatus).
		wantPresent []string
		// wantEventMsgAbsent lists substrings that must NOT appear in the
		// exception.message event attribute recorded by RecordError.
		wantEventMsgAbsent []string
		// wantEventMsgPresent lists substrings that MUST appear in the
		// exception.message event attribute recorded by RecordError.
		wantEventMsgPresent []string
		// wantExceptionType, when non-empty, is the exact value expected in
		// the exception.type event attribute (used to verify the none-branch fix).
		wantExceptionType string
	}{
		{
			name:               "none hides all content",
			privacyLevel:       PrivacyLevelNone,
			wantStatus:         "redacted",
			wantAbsent:         []string{"bob@example.com", "AKIA1234567890ABCDEF"},
			wantEventMsgAbsent: []string{"bob@example.com", "AKIA1234567890ABCDEF"},
			wantExceptionType:  "error",
		},
		{
			name:         "metadata redacts PII tokens",
			privacyLevel: PrivacyLevelMetadata,
			// Must not contain the raw values.
			wantAbsent:         []string{"bob@example.com", "AKIA1234567890ABCDEF"},
			wantEventMsgAbsent: []string{"bob@example.com", "AKIA1234567890ABCDEF"},
			// Must contain the redaction tokens inserted by DefaultPolicies.
			wantPresent:         []string{"[REDACTED_EMAIL]", "[REDACTED_AWS_KEY]"},
			wantEventMsgPresent: []string{"[REDACTED_EMAIL]", "[REDACTED_AWS_KEY]"},
		},
		{
			name:         "full exposes raw message",
			privacyLevel: PrivacyLevelFull,
			// Raw values must be present verbatim.
			wantPresent:         []string{"bob@example.com", "AKIA1234567890ABCDEF"},
			wantEventMsgPresent: []string{"bob@example.com", "AKIA1234567890ABCDEF"},
		},
		{
			name:                "empty level defaults to metadata behaviour",
			privacyLevel:        "",
			wantAbsent:          []string{"bob@example.com", "AKIA1234567890ABCDEF"},
			wantPresent:         []string{"[REDACTED_EMAIL]", "[REDACTED_AWS_KEY]"},
			wantEventMsgAbsent:  []string{"bob@example.com", "AKIA1234567890ABCDEF"},
			wantEventMsgPresent: []string{"[REDACTED_EMAIL]", "[REDACTED_AWS_KEY]"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.PrivacyLevel = tc.privacyLevel

			prov, exp := newTestProviderWithConfig(t, cfg)

			_, span := prov.StartRequestSpan(context.Background(), observability.RequestAttrs{})
			span.SetError(rawErr)
			span.End()

			got := exp.GetSpans()
			if len(got) != 1 {
				t.Fatalf("expected 1 span, got %d", len(got))
			}

			status := got[0].Status
			if status.Code != codes.Error {
				t.Fatalf("span status code = %v, want Error", status.Code)
			}

			desc := status.Description

			// Exact match when wantStatus is specified.
			if tc.wantStatus != "" && desc != tc.wantStatus {
				t.Errorf("status description = %q, want exactly %q", desc, tc.wantStatus)
			}

			// Substrings that must NOT be present in the status description.
			for _, absent := range tc.wantAbsent {
				if strings.Contains(desc, absent) {
					t.Errorf("status description %q contains forbidden substring %q", desc, absent)
				}
			}

			// Substrings that MUST be present in the status description.
			for _, present := range tc.wantPresent {
				if !strings.Contains(desc, present) {
					t.Errorf("status description %q is missing required substring %q", desc, present)
				}
			}

			// Assert on the exception.message event attribute recorded by RecordError.
			if len(got[0].Events) == 0 {
				t.Fatal("expected at least one span event (exception), got none")
			}
			eventAttrs := eventAttrMap(got[0].Events[0].Attributes)
			exceptionMsg := eventAttrs["exception.message"]

			for _, absent := range tc.wantEventMsgAbsent {
				if strings.Contains(exceptionMsg, absent) {
					t.Errorf("exception.message event attribute %q contains forbidden substring %q", exceptionMsg, absent)
				}
			}
			for _, present := range tc.wantEventMsgPresent {
				if !strings.Contains(exceptionMsg, present) {
					t.Errorf("exception.message event attribute %q is missing required substring %q", exceptionMsg, present)
				}
			}

			// For the none branch, verify exception.type is overridden to the
			// generic string "error" and does not expose the internal Go type path.
			if tc.wantExceptionType != "" {
				exceptionType := eventAttrs["exception.type"]
				if exceptionType != tc.wantExceptionType {
					t.Errorf("exception.type event attribute = %q, want %q", exceptionType, tc.wantExceptionType)
				}
			}
		})
	}
}

// TestSpan_StartChild_InheritsPrivacy verifies that a child span
// created via StartChild inherits the privacy level from its parent so
// SetError on the child is governed by the same policy.
func TestSpan_StartChild_InheritsPrivacy(t *testing.T) {
	cfg := DefaultConfig()
	cfg.PrivacyLevel = PrivacyLevelNone

	prov, exp := newTestProviderWithConfig(t, cfg)

	ctx, root := prov.StartRequestSpan(context.Background(), observability.RequestAttrs{})
	childCtx, child := root.StartChild(ctx, "child.op", observability.SpanKindClient)
	_ = childCtx

	child.SetError(errors.New("user secret@example.com token AKIA1234567890ABCDEF"))
	child.End()
	root.End()

	spans := exp.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("expected 2 spans, got %d", len(spans))
	}
	// Child ends first; spans[0] is the child span.
	childSpan := spans[0]
	if childSpan.Status.Code != codes.Error {
		t.Fatalf("child span status = %v, want Error", childSpan.Status.Code)
	}
	if childSpan.Status.Description != "redacted" {
		t.Errorf("child span description = %q, want %q", childSpan.Status.Description, "redacted")
	}
	if strings.Contains(childSpan.Status.Description, "secret@example.com") {
		t.Errorf("child span description leaks email: %q", childSpan.Status.Description)
	}

	// Also assert on the exception.message event attribute: none-level must not
	// expose the raw email or AWS key, and exception.type must be the generic "error".
	if len(childSpan.Events) == 0 {
		t.Fatal("expected at least one span event (exception) on child span, got none")
	}
	childEventAttrs := eventAttrMap(childSpan.Events[0].Attributes)
	childExceptionMsg := childEventAttrs["exception.message"]
	if strings.Contains(childExceptionMsg, "secret@example.com") {
		t.Errorf("child exception.message leaks email: %q", childExceptionMsg)
	}
	if strings.Contains(childExceptionMsg, "AKIA1234567890ABCDEF") {
		t.Errorf("child exception.message leaks AWS key: %q", childExceptionMsg)
	}
	if childEventAttrs["exception.type"] != "error" {
		t.Errorf("child exception.type = %q, want %q", childEventAttrs["exception.type"], "error")
	}
}

// TestConfigValidate tests the Config.Validate method.
func TestConfigValidate(t *testing.T) {
	tests := []struct {
		privacyLevel string
		wantErr      bool
	}{
		{"", false},
		{PrivacyLevelNone, false},
		{PrivacyLevelMetadata, false},
		{PrivacyLevelFull, false},
		{"bogus", true},
		{"FULL", true},
		{"None", true},
	}

	for _, tc := range tests {
		cfg := Config{PrivacyLevel: tc.privacyLevel}
		err := cfg.Validate()
		if tc.wantErr && err == nil {
			t.Errorf("Config{PrivacyLevel:%q}.Validate() = nil, want error", tc.privacyLevel)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("Config{PrivacyLevel:%q}.Validate() = %v, want nil", tc.privacyLevel, err)
		}
	}
}

// eventAttrMap converts a slice of event KeyValue attributes into a
// map of key → string value for convenient test assertions.
func eventAttrMap(kvs []attribute.KeyValue) map[string]string {
	m := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		m[string(kv.Key)] = kv.Value.Emit()
	}
	return m
}

// assertAttrs verifies that every want entry is present in got. Extra
// attributes in got are tolerated to keep tests forward-compatible.
func assertAttrs(t *testing.T, got []attribute.KeyValue, want map[string]attribute.Value) {
	t.Helper()
	gotMap := map[string]attribute.Value{}
	for _, kv := range got {
		gotMap[string(kv.Key)] = kv.Value
	}
	for k, v := range want {
		gv, ok := gotMap[k]
		if !ok {
			t.Errorf("missing attribute %q", k)
			continue
		}
		if gv.Emit() != v.Emit() {
			t.Errorf("attribute %q = %q, want %q", k, gv.Emit(), v.Emit())
		}
	}
}
