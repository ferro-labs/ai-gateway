package events

import (
	"strings"
	"testing"
	"time"
)

// fakeUpstreamKey has the legacy OpenAI sk- key shape. It is not a real credential.
const fakeUpstreamKey = "sk-abc123DEF456ghi789JKL012mno345"

// FailedRequest is the one point every failed-request event is built, so
// redacting here keeps raw upstream error text out of every hook consumer and
// observability exporter without each caller having to remember.
func TestFailedRequest_RedactsErrorMessage(t *testing.T) {
	he := FailedRequest(
		"trace-1",
		"openai",
		"gpt-4o",
		"openai API error (401): Incorrect API key provided: "+fakeUpstreamKey,
		250*time.Millisecond,
		false,
	)

	if strings.Contains(he.Error, fakeUpstreamKey) {
		t.Fatalf("HookEvent.Error leaked the key: %q", he.Error)
	}
	if !strings.Contains(he.Error, "[REDACTED_OPENAI_KEY]") {
		t.Errorf("HookEvent.Error = %q, want a redaction token", he.Error)
	}
}

// The redacted message must survive into the public map form exporters receive.
func TestFailedRequest_MapCarriesRedactedError(t *testing.T) {
	he := FailedRequest("trace-1", "openai", "gpt-4o", "bad key "+fakeUpstreamKey, time.Second, true)

	got, _ := he.Map()["error"].(string)
	if strings.Contains(got, fakeUpstreamKey) {
		t.Fatalf("event map leaked the key: %q", got)
	}
	if got == "" {
		t.Fatal("event map carried no error message")
	}
}

// Redaction must not disturb the rest of the payload.
func TestFailedRequest_RedactionPreservesOtherFields(t *testing.T) {
	he := FailedRequest("trace-1", "openai", "gpt-4o", "upstream is unavailable", 250*time.Millisecond, true)

	if he.Error != "upstream is unavailable" {
		t.Errorf("Error = %q, want it unchanged", he.Error)
	}
	if he.Subject != "gateway.request.failed" {
		t.Errorf("Subject = %q, want %q", he.Subject, "gateway.request.failed")
	}
	if he.TraceID != "trace-1" || he.Provider != "openai" || he.Model != "gpt-4o" {
		t.Errorf("identity fields changed: %+v", he)
	}
	if he.Status != 500 || he.LatencyMs != 250 || !he.Stream {
		t.Errorf("status/latency/stream changed: %+v", he)
	}
}
