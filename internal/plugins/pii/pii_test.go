package pii

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

const (
	piiEntityEmail = "email"
	piiEntitySSN   = "ssn"
)

func initPII(t *testing.T, config map[string]interface{}) *Plugin {
	t.Helper()
	p := &Plugin{}
	if err := p.Init(config); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	return p
}

func requestWithMessage(content string) *providers.Request {
	return &providers.Request{
		Model: "gpt-4o",
		Messages: []providers.Message{
			{Role: "user", Content: content},
		},
	}
}

func responseWithMessage(content string) *providers.Response {
	return &providers.Response{
		ID:    "resp-1",
		Model: "gpt-4o",
		Choices: []providers.Choice{
			{Index: 0, Message: providers.Message{Role: "assistant", Content: content}, FinishReason: "stop"},
		},
	}
}

func metadataTypes(t *testing.T, pctx *plugin.Context) []string {
	t.Helper()
	raw, ok := pctx.Metadata["pii_types"]
	if !ok {
		return nil
	}
	values, ok := raw.([]string)
	if !ok {
		t.Fatalf("pii_types has unexpected type %T", raw)
	}
	return values
}

func TestPIIPlugin_Init_Defaults(t *testing.T) {
	p := initPII(t, map[string]interface{}{})

	if p.action != "redact" {
		t.Fatalf("action = %q, want redact", p.action)
	}
	if p.redactMode != redactReplaceType {
		t.Fatalf("redactMode = %q, want replace_type", p.redactMode)
	}
	if p.applyTo != "input" {
		t.Fatalf("applyTo = %q, want input", p.applyTo)
	}
	if len(p.entities) != len(builtinEntities) {
		t.Fatalf("entities = %d, want %d", len(p.entities), len(builtinEntities))
	}
}

func TestPIIPlugin_Init_CustomEntities(t *testing.T) {
	p := initPII(t, map[string]interface{}{
		"entities": []interface{}{piiEntityEmail, piiEntitySSN},
	})

	if len(p.entities) != 2 {
		t.Fatalf("entities = %d, want 2", len(p.entities))
	}
	if p.entities[0].name != piiEntityEmail || p.entities[1].name != piiEntitySSN {
		t.Fatalf("unexpected entities: %v, %v", p.entities[0].name, p.entities[1].name)
	}
}

func TestPIIPlugin_BlockOnEmail(t *testing.T) {
	p := initPII(t, map[string]interface{}{"action": "block"})
	pctx := plugin.NewContext(requestWithMessage("email me at jane@example.com"))

	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !pctx.Reject {
		t.Fatal("expected request to be rejected")
	}
	if !strings.Contains(pctx.Reason, piiEntityEmail) {
		t.Fatalf("reason = %q, want email mention", pctx.Reason)
	}
}

func TestPIIPlugin_RedactEmail_ReplaceType(t *testing.T) {
	p := initPII(t, map[string]interface{}{"action": "redact", "redact_mode": "replace_type"})
	pctx := plugin.NewContext(requestWithMessage("contact jane@example.com now"))

	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got := pctx.Request.Messages[0].Content; !strings.Contains(got, "[EMAIL]") {
		t.Fatalf("content = %q, expected [EMAIL]", got)
	}
	if pctx.Metadata["pii_redacted"] != true {
		t.Fatalf("expected pii_redacted=true, got %v", pctx.Metadata["pii_redacted"])
	}
	if types := metadataTypes(t, pctx); len(types) == 0 || types[0] != piiEntityEmail {
		t.Fatalf("unexpected pii_types: %v", types)
	}
}

func TestPIIPlugin_RedactEmail_Mask(t *testing.T) {
	p := initPII(t, map[string]interface{}{"action": "redact", "redact_mode": "mask"})
	pctx := plugin.NewContext(requestWithMessage("contact jane@example.com now"))

	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got := pctx.Request.Messages[0].Content; !strings.Contains(got, "***") {
		t.Fatalf("content = %q, expected masked value", got)
	}
}

func TestPIIPlugin_RedactEmail_Hash(t *testing.T) {
	p := initPII(t, map[string]interface{}{"action": "redact", "redact_mode": "hash"})
	pctx := plugin.NewContext(requestWithMessage("contact jane@example.com now"))

	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	matched, err := regexp.MatchString(`\[EMAIL:[a-f0-9]{6}\]`, pctx.Request.Messages[0].Content)
	if err != nil {
		t.Fatalf("regex error: %v", err)
	}
	if !matched {
		t.Fatalf("content = %q, expected [EMAIL:xxxxxx] format", pctx.Request.Messages[0].Content)
	}
}

func TestPIIPlugin_RedactEmail_Synthetic(t *testing.T) {
	p := initPII(t, map[string]interface{}{"action": "redact", "redact_mode": "synthetic"})
	pctx := plugin.NewContext(requestWithMessage("contact jane@example.com now"))

	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got := pctx.Request.Messages[0].Content; !strings.Contains(got, "user1@example.com") {
		t.Fatalf("content = %q, expected synthetic email", got)
	}
}

func TestPIIPlugin_RedactPhone(t *testing.T) {
	p := initPII(t, map[string]interface{}{"action": "redact", "redact_mode": "replace_type"})
	pctx := plugin.NewContext(requestWithMessage("call me at (415) 555-1212"))

	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got := pctx.Request.Messages[0].Content; !strings.Contains(got, "[PHONE]") {
		t.Fatalf("content = %q, expected [PHONE]", got)
	}
}

func TestPIIPlugin_RedactCreditCard_LuhnInvalidNotDetected(t *testing.T) {
	p := initPII(t, map[string]interface{}{
		"action":   "block",
		"entities": []interface{}{"credit_card"},
	})
	pctx := plugin.NewContext(requestWithMessage("card 4111111111111112"))

	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if pctx.Reject {
		t.Fatal("expected invalid luhn card to pass through")
	}
}

func TestPIIPlugin_BlockCreditCard_EntityOverride(t *testing.T) {
	p := initPII(t, map[string]interface{}{
		"action": "redact",
		"entity_overrides": map[string]interface{}{
			"credit_card": map[string]interface{}{"action": "block"},
		},
	})
	pctx := plugin.NewContext(requestWithMessage("card 4111111111111111"))

	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !pctx.Reject {
		t.Fatal("expected entity override to block credit card")
	}
}

func TestPIIPlugin_RedactSSN(t *testing.T) {
	p := initPII(t, map[string]interface{}{"action": "redact", "redact_mode": "replace_type"})
	pctx := plugin.NewContext(requestWithMessage("ssn 123-45-6789"))

	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got := pctx.Request.Messages[0].Content; !strings.Contains(got, "[SSN]") {
		t.Fatalf("content = %q, expected [SSN]", got)
	}
}

func TestPIIPlugin_NoMatchPassesThrough(t *testing.T) {
	p := initPII(t, map[string]interface{}{"action": "block"})
	pctx := plugin.NewContext(requestWithMessage("hello world"))

	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if pctx.Reject {
		t.Fatal("expected clean request to pass through")
	}
}

func TestPIIPlugin_WarnMode_NoReject_MetadataSet(t *testing.T) {
	p := initPII(t, map[string]interface{}{"action": "warn"})
	pctx := plugin.NewContext(requestWithMessage("email jane@example.com"))

	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if pctx.Reject {
		t.Fatal("expected warn mode not to reject")
	}
	if types := metadataTypes(t, pctx); len(types) == 0 || types[0] != piiEntityEmail {
		t.Fatalf("unexpected pii_types: %v", types)
	}
}

func TestPIIPlugin_ApplyToOutput_SkipsInputScan(t *testing.T) {
	p := initPII(t, map[string]interface{}{"action": "block", "apply_to": "output"})
	pctx := plugin.NewContext(requestWithMessage("email jane@example.com"))

	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if pctx.Reject {
		t.Fatal("expected input scan to be skipped when apply_to=output")
	}
}

func TestPIIPlugin_ApplyToBoth_ScansInputAndOutput(t *testing.T) {
	p := initPII(t, map[string]interface{}{"action": "redact", "apply_to": "both", "redact_mode": "replace_type"})
	pctx := plugin.NewContext(requestWithMessage("input jane@example.com"))

	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute input error: %v", err)
	}
	if !strings.Contains(pctx.Request.Messages[0].Content, "[EMAIL]") {
		t.Fatalf("input content = %q, expected [EMAIL]", pctx.Request.Messages[0].Content)
	}

	pctx.Response = responseWithMessage("output 123-45-6789")
	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute output error: %v", err)
	}
	if !strings.Contains(pctx.Response.Choices[0].Message.Content, "[SSN]") {
		t.Fatalf("output content = %q, expected [SSN]", pctx.Response.Choices[0].Message.Content)
	}

	types := metadataTypes(t, pctx)
	if len(types) != 2 || types[0] != piiEntityEmail || types[1] != piiEntitySSN {
		t.Fatalf("unexpected merged pii_types: %v", types)
	}
}

func TestPIIPlugin_MultipleEntitiesInOneMessage(t *testing.T) {
	p := initPII(t, map[string]interface{}{"action": "block"})
	pctx := plugin.NewContext(requestWithMessage("email jane@example.com and ssn 123-45-6789"))

	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !pctx.Reject {
		t.Fatal("expected message to be rejected")
	}
	if !strings.Contains(pctx.Reason, piiEntityEmail) || !strings.Contains(pctx.Reason, piiEntitySSN) {
		t.Fatalf("reason = %q, expected both entity names", pctx.Reason)
	}
}

func TestPIIPlugin_NilRequest(t *testing.T) {
	p := initPII(t, map[string]interface{}{"action": "block"})
	pctx := plugin.NewContext(nil)

	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if pctx.Reject {
		t.Fatal("expected nil request to pass through")
	}
}
