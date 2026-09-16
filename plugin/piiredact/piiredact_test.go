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

func TestExecute_RedactRewritesOnAChatShapedSurface(t *testing.T) {
	p := &PIIRedact{}
	if err := p.Init(map[string]any{"action": "redact"}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// No plugin.MetadataSurface key: the chat surfaces read the rewritten
	// request back, so a rewrite here is the one that actually travels.
	pctx := newRequest("my ssn is 123-45-6789")
	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if pctx.Reject {
		t.Fatalf("a chat-shaped request was denied instead of sanitized: %q", pctx.Reason)
	}
	if got := pctx.Request.Messages[0].Content; strings.Contains(got, "123-45-6789") {
		t.Fatalf("content %q still carries the SSN — redaction did not rewrite the request", got)
	}
}

func TestExecute_RedactDeniesOnASurfaceThatDiscardsTheRewrite(t *testing.T) {
	p := &PIIRedact{}
	if err := p.Init(map[string]any{"action": "redact"}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newRequest("customer 123-45-6789 called")
	pctx.Metadata[plugin.MetadataSurface] = "embeddings"
	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute returned an error; a denial is a verdict: %v", err)
	}

	if !pctx.Reject {
		t.Fatal("the SSN was forwarded to the provider while the plugin reported a redaction that this surface discards")
	}
	if got := pctx.Request.Messages[0].Content; !strings.Contains(got, "123-45-6789") {
		t.Fatalf("content %q was rewritten on a surface that reads the rewrite back — the rewrite is pointless work and hides the denial", got)
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
				// Two parts carrying different values: one part cannot show that
				// the rewrite iterates past the first, and redacting only the
				// first would forward the second to the provider while the log
				// still reported a redaction.
				ContentParts: []providers.ContentPart{
					{Type: "input_audio", Text: "call 555-123-4567"},
					{Type: "input_audio", Text: "ssn is 123-45-6789"},
				},
			}},
		},
	}
	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	parts := pctx.Request.Messages[0].ContentParts
	if got := parts[0].Text; strings.Contains(got, "555-123-4567") {
		t.Fatalf("part text %q was not redacted — a non-text part leaves no trace in Content", got)
	}
	if got := parts[1].Text; strings.Contains(got, "123-45-6789") {
		t.Fatalf("second part text %q was not redacted — the rewrite stopped at the first part", got)
	}
}

// Redaction has to cover every field the plugin screens. A field block mode
// denies and redact mode leaves alone is the value forwarded upstream by the
// mode whose whole purpose is that the provider never sees it.
func TestExecute_RedactsToolCallArgumentsAndReasoning(t *testing.T) {
	p := &PIIRedact{}
	if err := p.Init(map[string]any{"action": "redact"}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := &plugin.Context{
		Stage:    plugin.StageBeforeRequest,
		Metadata: map[string]any{},
		Request: &providers.Request{
			Messages: []providers.Message{{
				Content:          "look it up",
				ReasoningContent: "their ssn is 123-45-6789",
				ToolCalls: []providers.ToolCall{{Function: providers.FunctionCall{
					Name:      "lookup",
					Arguments: `{"phone":"555-123-4567"}`,
				}}},
			}},
		},
	}
	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	msg := pctx.Request.Messages[0]
	if strings.Contains(msg.ReasoningContent, "123-45-6789") {
		t.Fatalf("reasoning content %q was not redacted", msg.ReasoningContent)
	}
	if got := msg.ToolCalls[0].Function.Arguments; strings.Contains(got, "555-123-4567") {
		t.Fatalf("tool-call arguments %q were not redacted", got)
	}
	if pctx.Reject {
		t.Fatalf("redact mode denied the request: %q", pctx.Reason)
	}
}

func TestInit_EntitiesSelectsASubset(t *testing.T) {
	p := &PIIRedact{}
	if err := p.Init(map[string]any{"action": "block", "entities": []any{"ssn"}}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newRequest("email me at bob@acme.com")
	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute returned an error; a denial is a verdict, not a fault: %v", err)
	}

	if pctx.Reject {
		t.Fatal("an email was blocked although entities selected ssn only")
	}
}

func TestInit_RejectsAnUnknownEntityName(t *testing.T) {
	p := &PIIRedact{}
	err := p.Init(map[string]any{"entities": []any{"social_security"}})

	if err == nil {
		t.Fatal("Init accepted an unknown entity name; it selects nothing, so the plugin reports itself enabled and screens nothing")
	}
	if !strings.Contains(err.Error(), "social_security") {
		t.Fatalf("error %q does not name the offending value", err.Error())
	}
	if !strings.Contains(err.Error(), "ssn") {
		t.Fatalf("error %q does not name the accepted set", err.Error())
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
	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute returned an error; a denial is a verdict, not a fault: %v", err)
	}

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
	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute returned an error; a denial is a verdict, not a fault: %v", err)
	}

	if !pctx.Reject {
		t.Fatal("uninspectable content was forwarded unscreened")
	}
}

func TestInit_RejectsAnEmptyEntitiesListWithNoCustomPatterns(t *testing.T) {
	p := &PIIRedact{}
	// Present and empty is not the same as absent. An absent key selects every
	// built-in entity; an empty list with nothing to fall back on yields a
	// plugin the catalog reports as enabled that detects nothing.
	err := p.Init(map[string]any{"entities": []any{}})

	if err == nil {
		t.Fatal("Init accepted an empty entities list; it yields a guardrail that enforces nothing")
	}
}

func TestInit_EmptyEntitiesIsLegalAlongsideCustomPatterns(t *testing.T) {
	p := &PIIRedact{}
	// "Screen my patterns and none of the built-ins" is a real policy, and the
	// plugin ends up with a detector, so it must load and enforce.
	if err := p.Init(map[string]any{
		"action":   "block",
		"entities": []any{},
		"patterns": []any{`\bACME-\d{6}\b`},
	}); err != nil {
		t.Fatalf("Init rejected an empty entities list carrying a custom pattern; that config screens something: %v", err)
	}

	pctx := newRequest("the record is ACME-123456")
	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !pctx.Reject {
		t.Fatal("a custom pattern did not screen with entities explicitly empty")
	}
}

func TestInit_RejectsAnEntitiesValueThatIsNotAList(t *testing.T) {
	p := &PIIRedact{}
	// One name written without the list syntax. Widening it to every built-in
	// enables detectors the operator never asked for — credit_card matches any
	// sixteen-digit order number.
	err := p.Init(map[string]any{"entities": "ssn"})

	if err == nil {
		t.Fatal("Init accepted an entities value that is not a list; a scalar must fail the load, not silently select every entity")
	}
}

// A scalar written where a string belongs is a different fact from an absent
// key, and only one of them is a configuration. Discarding the type
// assertion's second result reads `action: 1` as "not set", so the plugin
// loads, reports itself enabled, and enforces the default the operator was
// overriding.
func TestInit_RejectsANonStringAction(t *testing.T) {
	p := &PIIRedact{}
	err := p.Init(map[string]any{"action": 1})

	if err == nil {
		t.Fatal("Init accepted a non-string action; a present-but-wrong-typed key silently takes the default")
	}
	if !strings.Contains(err.Error(), "action") {
		t.Fatalf("error does not name the key: %v", err)
	}
}

func TestInit_RejectsANonStringPlaceholder(t *testing.T) {
	p := &PIIRedact{}
	err := p.Init(map[string]any{"action": "redact", "redact_placeholder": 0})

	if err == nil {
		t.Fatal("Init accepted a non-string redact_placeholder; the configured placeholder silently reverts to the default")
	}
	if !strings.Contains(err.Error(), "redact_placeholder") {
		t.Fatalf("error does not name the key: %v", err)
	}
}

func TestInit_RejectsAPatternsValueThatIsNotAList(t *testing.T) {
	p := &PIIRedact{}
	// One pattern written without the list syntax. Reading it as "no custom
	// patterns" loads a plugin screening for none of what the operator wrote.
	err := p.Init(map[string]any{"patterns": `\bACME-\d{6}\b`})

	if err == nil {
		t.Fatal("Init accepted a patterns value that is not a list; the custom patterns are silently dropped")
	}
	if !strings.Contains(err.Error(), "patterns") {
		t.Fatalf("error does not name the key: %v", err)
	}
}
