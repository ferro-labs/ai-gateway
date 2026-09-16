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

// A card number is sixteen digits whose check digit passes Luhn. A number that
// does not is an order id, a tracking number or a phone number written without
// separators, and denying it is a false positive that costs a caller their
// request.
func TestExecute_CreditCardRequiresAValidCheckDigit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		text   string
		reject bool
	}{
		{"valid card with spaces", "pay with 4111 1111 1111 1111 today", true},
		{"valid card with dashes", "pay with 5500-0000-0000-0004 today", true},
		{"sixteen digits failing the check", "order 1234 5678 9012 3456 shipped", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &PIIRedact{}
			if err := p.Init(map[string]any{"entities": []any{"credit_card"}}); err != nil {
				t.Fatalf("Init: %v", err)
			}
			pctx := newRequest(tc.text)
			if err := p.Execute(context.Background(), pctx); err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if pctx.Reject != tc.reject {
				t.Fatalf("Reject = %v, want %v for %q", pctx.Reject, tc.reject, tc.text)
			}
		})
	}
}

func TestExecute_RedactLeavesANumberFailingTheCheckDigitAlone(t *testing.T) {
	p := &PIIRedact{}
	if err := p.Init(map[string]any{"entities": []any{"credit_card"}, "action": "redact"}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	pctx := newRequest("order 1234 5678 9012 3456 and card 4111 1111 1111 1111")
	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := pctx.Request.Messages[0].Content
	want := "order 1234 5678 9012 3456 and card [REDACTED]"
	if got != want {
		t.Fatalf("Content = %q, want %q", got, want)
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

// The placeholder is literal text, not a replacement template. Expanded as a
// template, "$0" stands for the whole match — so the configured placeholder put
// the detected value straight back into the request while the plugin logged a
// redaction and let the request through.
func TestExecute_RedactDoesNotExpandThePlaceholderAsATemplate(t *testing.T) {
	p := &PIIRedact{}
	if err := p.Init(map[string]any{"action": "redact", "redact_placeholder": "$0"}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newRequest("my ssn is 123-45-6789")
	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	got := pctx.Request.Messages[0].Content
	if strings.Contains(got, "123-45-6789") {
		t.Fatalf("content %q still carries the value the plugin reported as redacted", got)
	}
	if !strings.Contains(got, "$0") {
		t.Fatalf("content %q does not carry the configured placeholder", got)
	}
}

// A placeholder carrying a dollar sign is ordinary text an operator wrote, and
// it must reach the provider exactly as configured.
func TestExecute_RedactInsertsAPlaceholderCarryingADollarSignVerbatim(t *testing.T) {
	p := &PIIRedact{}
	if err := p.Init(map[string]any{"action": "redact", "redact_placeholder": "[$$$]"}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newRequest("my ssn is 123-45-6789")
	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if got, want := pctx.Request.Messages[0].Content, "my ssn is [$$$]"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
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

// An empty pattern compiles and matches every string, so it registers a plugin
// that denies every request under the default action and rewrites every
// request under redact. An empty entry in a list is a typo — a trailing comma,
// a blank list item — never a policy.
func TestInit_RejectsAnEmptyCustomPattern(t *testing.T) {
	for _, pattern := range []string{"", "   "} {
		p := &PIIRedact{}
		err := p.Init(map[string]any{"patterns": []any{pattern}})

		if err == nil {
			t.Fatalf("Init accepted the empty pattern %q; it matches every request", pattern)
		}
		if !strings.Contains(err.Error(), "patterns[0]") {
			t.Fatalf("error does not name the entry: %v", err)
		}
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

// A plugin runs inside the request pipeline, so it stops when the request is
// abandoned rather than screening or rewriting content nobody is waiting for.
//
// It returns nil, not the context's error: an error from Execute means the
// plugin BROKE, which the gateway reports as a 500 and counts against the
// target's circuit breaker. A caller hanging up is not a server fault.
func TestExecute_StopsOnACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	t.Run("block", func(t *testing.T) {
		p := &PIIRedact{}
		if err := p.Init(map[string]any{"action": "block"}); err != nil {
			t.Fatalf("Init: %v", err)
		}

		pctx := newRequest("my ssn is 123-45-6789")
		if err := p.Execute(ctx, pctx); err != nil {
			t.Fatalf("Execute returned the caller's cancellation as a plugin fault: %v", err)
		}
		if pctx.Reject {
			t.Fatal("the screening loop ran to completion on an abandoned request")
		}
	})

	t.Run("redact", func(t *testing.T) {
		p := &PIIRedact{}
		if err := p.Init(map[string]any{"action": "redact"}); err != nil {
			t.Fatalf("Init: %v", err)
		}

		pctx := newRequest("my ssn is 123-45-6789")
		if err := p.Execute(ctx, pctx); err != nil {
			t.Fatalf("Execute returned the caller's cancellation as a plugin fault: %v", err)
		}
		if !strings.Contains(pctx.Request.Messages[0].Content, "123-45-6789") {
			t.Fatal("the rewrite ran to completion on an abandoned request")
		}
	})
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

// A misspelled action is caught by `ferrogw validate`, rather than at the
// startup or the config reload that follows it. A ${VAR} reference is passed
// instead of checked: it resolves when the plugin is constructed, so validate
// cannot read one and must not be stricter than the server it checks for.
func TestValidateConfig_CatchesAMisspelledActionAndPassesAnEnvReference(t *testing.T) {
	if err := plugin.ValidateConfigFor("pii-redact", map[string]any{"action": "redactt"}); err == nil {
		t.Fatal("a misspelled action was reported valid; an operator meets it at startup instead")
	}
	if err := plugin.ValidateConfigFor("pii-redact", map[string]any{"action": "${GUARDRAIL_ACTION}"}); err != nil {
		t.Fatalf("an env reference was rejected at load, where it is not yet resolved: %v", err)
	}
}
