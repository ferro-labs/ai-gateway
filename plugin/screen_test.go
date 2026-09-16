package plugin

import (
	"slices"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

func TestRequestText_YieldsContentAndEveryTextPart(t *testing.T) {
	req := &providers.Request{
		Messages: []providers.Message{
			{Content: "collapsed text"},
			{ContentParts: []providers.ContentPart{
				{Type: "text", Text: "part one"},
				{Type: "input_audio", Text: "part two"},
				{Type: "image_url", ImageURL: &providers.ImageURLPart{URL: "data:image/png;base64,AAAA"}},
			}},
		},
	}

	got := slices.Collect(RequestText(req))

	want := []string{"collapsed text", "part one", "part two"}
	if !slices.Equal(got, want) {
		t.Fatalf("RequestText yielded %q, want %q — ImageURL must not be screened and every part type must be", got, want)
	}
}

func TestRequestText_NilRequestYieldsNothing(t *testing.T) {
	if n := len(slices.Collect(RequestText(nil))); n != 0 {
		t.Fatalf("RequestText(nil) yielded %d strings, want 0", n)
	}
}

func TestRejectUninspectable_DeniesBeforeRequestWithAVerdictNotAnError(t *testing.T) {
	pctx := &Context{
		Stage:    StageBeforeRequest,
		Metadata: map[string]any{MetadataUninspectableContent: true},
	}

	if !RejectUninspectable(pctx) {
		t.Fatal("RejectUninspectable returned false for uninspectable content at before_request")
	}
	if !pctx.Reject {
		t.Fatal("pctx.Reject not set — an unreadable body must be denied, not forwarded unscreened")
	}
	if pctx.Reason == "" {
		t.Fatal("pctx.Reason empty — the caller must learn that sending text fixes the request")
	}
}

func TestRejectUninspectable_IgnoresAfterRequestStage(t *testing.T) {
	pctx := &Context{
		Stage:    StageAfterRequest,
		Metadata: map[string]any{MetadataUninspectableContent: true},
	}

	if RejectUninspectable(pctx) {
		t.Fatal("RejectUninspectable denied at after_request — the response has already been delivered")
	}
}

func TestRejectUninspectable_AllowsInspectableContent(t *testing.T) {
	pctx := &Context{Stage: StageBeforeRequest, Metadata: map[string]any{}}

	if RejectUninspectable(pctx) {
		t.Fatal("RejectUninspectable denied a request whose content was readable")
	}
}

func TestNormalizeAction_EmptyTakesTheFallback(t *testing.T) {
	got, err := NormalizeAction("", ActionWarn, ActionBlock, ActionWarn, ActionLog)
	if err != nil {
		t.Fatalf("NormalizeAction(\"\", ...): %v", err)
	}
	if got != ActionWarn {
		t.Fatalf("NormalizeAction(\"\", %q, ...) = %q, want the fallback", ActionWarn, got)
	}
}

func TestNormalizeAction_AcceptsEachClosedSetValue(t *testing.T) {
	for _, action := range []string{ActionBlock, ActionWarn, ActionLog} {
		got, err := NormalizeAction(action, ActionBlock, ActionBlock, ActionWarn, ActionLog)
		if err != nil {
			t.Fatalf("NormalizeAction(%q, ...): %v", action, err)
		}
		if got != action {
			t.Fatalf("NormalizeAction(%q, ...) = %q, want %q", action, got, action)
		}
	}
}

func TestNormalizeAction_TrimsAndLowercases(t *testing.T) {
	got, err := NormalizeAction("  BLOCK  ", ActionWarn, ActionBlock, ActionWarn, ActionLog)
	if err != nil {
		t.Fatalf("NormalizeAction: %v", err)
	}
	if got != ActionBlock {
		t.Fatalf("NormalizeAction(\"  BLOCK  \", ...) = %q, want %q", got, ActionBlock)
	}
}

func TestNormalizeAction_RejectsAnUnrecognisedValue(t *testing.T) {
	_, err := NormalizeAction("blockk", ActionBlock, ActionBlock, ActionWarn, ActionLog)
	if err == nil {
		t.Fatal("NormalizeAction accepted \"blockk\"; a misspelled action must fail the load, not degrade to a non-blocking default")
	}
	if !strings.Contains(err.Error(), "blockk") {
		t.Fatalf("error %q does not name the offending value", err.Error())
	}
	for _, want := range []string{ActionBlock, ActionWarn, ActionLog} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not name the accepted set (missing %q)", err.Error(), want)
		}
	}
}

func TestNormalizeAction_RejectsAnActionValidForAnotherPlugin(t *testing.T) {
	// "redact" is a real action — for pii-redact, not for a filter whose
	// allowed set is {block, warn, log}. The caller's set must be the one
	// enforced, not some global union of every plugin's vocabulary.
	_, err := NormalizeAction("redact", ActionBlock, ActionBlock, ActionWarn, ActionLog)
	if err == nil {
		t.Fatal("NormalizeAction accepted \"redact\" against a caller whose allowed set does not include it")
	}
}
