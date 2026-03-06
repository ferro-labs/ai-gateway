package plugin

import "testing"

func TestRejectionError_Error_BeforeRequest(t *testing.T) {
	err := (&RejectionError{
		Plugin: "guardrail-a",
		Stage:  StageBeforeRequest,
		Reason: "blocked input",
	}).Error()

	want := "request rejected by guardrail-a (before_request): blocked input"
	if err != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
}

func TestRejectionError_Error_AfterRequest(t *testing.T) {
	err := (&RejectionError{
		Plugin: "guardrail-b",
		Stage:  StageAfterRequest,
		Reason: "schema mismatch",
	}).Error()

	want := "response rejected by guardrail-b (after_request): schema mismatch"
	if err != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
}

func TestRejectionError_Error_UnknownStage(t *testing.T) {
	err := (&RejectionError{
		Plugin: "guardrail-c",
		Stage:  Stage("custom_stage"),
		Reason: "custom",
	}).Error()

	want := "rejected by guardrail-c (custom_stage): custom"
	if err != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
}
