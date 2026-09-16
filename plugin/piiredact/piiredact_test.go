package piiredact

import (
	"context"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

func newRequest(content string) *plugin.Context {
	return &plugin.Context{
		Stage:    plugin.StageBeforeRequest,
		Metadata: map[string]any{},
		Request: &providers.Request{
			Messages: []providers.Message{{Content: content}},
		},
	}
}

func TestExecute_BlocksDetectedSSN(t *testing.T) {
	p := &PIIRedact{}
	if err := p.Init(map[string]any{"action": "block"}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newRequest("my ssn is 123-45-6789")
	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute returned an error; a denial is a verdict: %v", err)
	}

	if !pctx.Reject {
		t.Fatal("an SSN reached the provider with action=block")
	}
}

func TestExecute_RedactRewritesTheRequestAndAllowsIt(t *testing.T) {
	p := &PIIRedact{}
	if err := p.Init(map[string]any{"action": "redact"}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newRequest("email me at bob@acme.com please")
	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if pctx.Reject {
		t.Fatal("action=redact rejected the request; it must sanitize and continue")
	}
	got := pctx.Request.Messages[0].Content
	if strings.Contains(got, "bob@acme.com") {
		t.Fatalf("content %q still carries the address — redaction did not rewrite the request", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("content %q carries no placeholder", got)
	}
}

func TestExecute_RedactsEveryContentPartNotJustContent(t *testing.T) {
	p := &PIIRedact{}
	if err := p.Init(map[string]any{"action": "redact"}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := &plugin.Context{
		Stage:    plugin.StageBeforeRequest,
		Metadata: map[string]any{},
		Request: &providers.Request{
			Messages: []providers.Message{{
				ContentParts: []providers.ContentPart{
					{Type: "input_audio", Text: "call 555-123-4567"},
				},
			}},
		},
	}
	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if got := pctx.Request.Messages[0].ContentParts[0].Text; strings.Contains(got, "555-123-4567") {
		t.Fatalf("part text %q was not redacted — a non-text part leaves no trace in Content", got)
	}
}

func TestInit_EntitiesSelectsASubset(t *testing.T) {
	p := &PIIRedact{}
	if err := p.Init(map[string]any{"action": "block", "entities": []any{"ssn"}}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newRequest("email me at bob@acme.com")
	_ = p.Execute(context.Background(), pctx)

	if pctx.Reject {
		t.Fatal("an email was blocked although entities selected ssn only")
	}
}

func TestInit_RejectsAnUncompilableCustomPattern(t *testing.T) {
	p := &PIIRedact{}
	err := p.Init(map[string]any{"patterns": []any{"([unclosed"}})

	if err == nil {
		t.Fatal("Init accepted an uncompilable custom pattern")
	}
}

func TestExecute_ReasonNamesTheEntityTypeNotTheValue(t *testing.T) {
	p := &PIIRedact{}
	if err := p.Init(map[string]any{"action": "block"}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newRequest("my ssn is 123-45-6789")
	_ = p.Execute(context.Background(), pctx)

	if strings.Contains(pctx.Reason, "123-45-6789") {
		t.Fatalf("Reason %q echoes the detected value back to the caller", pctx.Reason)
	}
	if !strings.Contains(pctx.Reason, "ssn") {
		t.Fatalf("Reason %q does not say which entity type was detected, so the caller cannot fix the request", pctx.Reason)
	}
}

func TestExecute_DeniesUninspectableContent(t *testing.T) {
	p := &PIIRedact{}
	if err := p.Init(map[string]any{"action": "block"}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newRequest("")
	pctx.Metadata[plugin.MetadataUninspectableContent] = true
	_ = p.Execute(context.Background(), pctx)

	if !pctx.Reject {
		t.Fatal("uninspectable content was forwarded unscreened")
	}
}
