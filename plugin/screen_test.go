package plugin

import (
	"slices"
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
