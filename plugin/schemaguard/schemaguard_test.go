package schemaguard

import (
	"context"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

func newResponse(content string) *plugin.Context {
	return &plugin.Context{
		Stage:    plugin.StageAfterRequest,
		Metadata: map[string]any{},
		Response: &providers.Response{
			Choices: []providers.Choice{{Message: providers.Message{Content: content}}},
		},
	}
}

// newChoice builds one choice carrying whatever the caller gives it, which is
// how a response split across content parts is expressed.
func newChoice(msg providers.Message) *plugin.Context {
	return &plugin.Context{
		Stage:    plugin.StageAfterRequest,
		Metadata: map[string]any{},
		Response: &providers.Response{Choices: []providers.Choice{{Message: msg}}},
	}
}

func objectSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []any{"name", "score"},
		"properties": map[string]any{
			"name":  map[string]any{"type": "string"},
			"score": map[string]any{"type": "number"},
		},
	}
}

func TestExecute_AllowsAConformingResponse(t *testing.T) {
	g := &SchemaGuard{}
	if err := g.Init(map[string]any{"schema": objectSchema()}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newResponse(`{"name":"ada","score":9.5}`)
	if err := g.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if pctx.Reject {
		t.Fatalf("a conforming response was rejected: %q", pctx.Reason)
	}
}

func TestExecute_RejectsAResponseMissingARequiredField(t *testing.T) {
	g := &SchemaGuard{}
	if err := g.Init(map[string]any{"schema": objectSchema()}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newResponse(`{"name":"ada"}`)
	if err := g.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute returned an error; a denial is a verdict, not a fault: %v", err)
	}

	if !pctx.Reject {
		t.Fatal("a response missing a required field was allowed through")
	}
}

func TestExecute_RejectsAWronglyTypedField(t *testing.T) {
	g := &SchemaGuard{}
	if err := g.Init(map[string]any{"schema": objectSchema()}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newResponse(`{"name":"ada","score":"high"}`)
	if err := g.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute returned an error; a denial is a verdict, not a fault: %v", err)
	}

	if !pctx.Reject {
		t.Fatal("a string where the schema requires a number was allowed through")
	}
}

func TestExecute_RejectsUnparseableJSON(t *testing.T) {
	g := &SchemaGuard{}
	if err := g.Init(map[string]any{"schema": objectSchema()}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newResponse("I'm afraid I can't do that")
	if err := g.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute returned an error; a denial is a verdict, not a fault: %v", err)
	}

	if !pctx.Reject {
		t.Fatal("prose was allowed through where an object was required")
	}
}

func TestExecute_ReasonNamesTheViolation(t *testing.T) {
	g := &SchemaGuard{}
	if err := g.Init(map[string]any{"schema": objectSchema()}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newResponse(`{"name":"ada"}`)
	if err := g.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute returned an error; a denial is a verdict, not a fault: %v", err)
	}

	if !pctx.Reject {
		t.Fatal("a response missing a required field was allowed through")
	}
	// The field name is the whole diagnostic value here. Asserting only that
	// the reason is non-empty passes on any string at all, including one that
	// names a different field.
	if !strings.Contains(pctx.Reason, "score") {
		t.Fatalf("Reason %q does not name the missing field, so the caller cannot tell which one was wrong", pctx.Reason)
	}
}

// integerSchema is the ordinary shape for a count, an age or an id. JSON has
// one number type on the wire, so a validator built on encoding/json sees
// float64 for both 42 and 42.5 and has to tell them apart by value.
func integerSchema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"age": map[string]any{"type": "integer"}},
	}
}

func TestExecute_AcceptsAWholeNumberWhereTheSchemaSaysInteger(t *testing.T) {
	g := &SchemaGuard{}
	if err := g.Init(map[string]any{"schema": integerSchema()}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newResponse(`{"age":42}`)
	if err := g.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if pctx.Reject {
		t.Fatalf("a whole number was rejected where the schema says integer: %q", pctx.Reason)
	}
}

func TestExecute_RejectsAFractionalValueWhereTheSchemaSaysInteger(t *testing.T) {
	g := &SchemaGuard{}
	if err := g.Init(map[string]any{"schema": integerSchema()}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newResponse(`{"age":42.5}`)
	if err := g.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute returned an error; a denial is a verdict, not a fault: %v", err)
	}

	if !pctx.Reject {
		t.Fatal("a fractional value was allowed where the schema requires an integer")
	}
	if !strings.Contains(pctx.Reason, "response.age") {
		t.Fatalf("Reason does not name the field: %q", pctx.Reason)
	}
}

func TestExecute_AcceptsAWholeNumberWhereTheSchemaSaysNumber(t *testing.T) {
	g := &SchemaGuard{}
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"age": map[string]any{"type": "number"}},
	}
	if err := g.Init(map[string]any{"schema": schema}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newResponse(`{"age":42}`)
	if err := g.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if pctx.Reject {
		t.Fatalf("a whole number was rejected where the schema says number: %q", pctx.Reason)
	}
}

func TestExecute_WarnActionDoesNotReject(t *testing.T) {
	g := &SchemaGuard{}
	if err := g.Init(map[string]any{"schema": objectSchema(), "action": "warn"}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newResponse(`{"name":"ada"}`)
	if err := g.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute returned an error; a denial is a verdict, not a fault: %v", err)
	}

	if pctx.Reject {
		t.Fatal("action=warn rejected a non-conforming response")
	}
}

// A schema is a statement about a DOCUMENT, so the parts of a choice have to
// be assembled before they are validated. Validated separately, each half of a
// split object is invalid JSON on its own and a conforming response is denied.
func TestExecute_AssemblesADocumentSplitAcrossContentParts(t *testing.T) {
	g := &SchemaGuard{}
	if err := g.Init(map[string]any{"schema": objectSchema()}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newChoice(providers.Message{ContentParts: []providers.ContentPart{
		{Type: "text", Text: `{"name":"ada",`},
		{Type: "text", Text: `"score":9.5}`},
	}})
	if err := g.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if pctx.Reject {
		t.Fatalf("a conforming document split across parts was rejected: %q", pctx.Reason)
	}
}

// A decoded multipart message carries BOTH the collapsed Content and the parts
// it was collapsed from. Validating each string the choice yields validates the
// same document twice, and the second pass rejects it as a duplicate fragment.
func TestExecute_ValidatesACollapsedDocumentOnce(t *testing.T) {
	g := &SchemaGuard{}
	if err := g.Init(map[string]any{"schema": objectSchema()}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newChoice(providers.Message{
		Content: `{"name":"ada","score":9.5}`,
		ContentParts: []providers.ContentPart{
			{Type: "text", Text: `{"name":"ada",`},
			{Type: "text", Text: `"score":9.5}`},
		},
	})
	if err := g.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if pctx.Reject {
		t.Fatalf("a conforming response was rejected on a second pass over its own fragments: %q", pctx.Reason)
	}
}

// A choice carrying nothing does not satisfy a schema requiring an object.
// Skipping it read an empty answer as a conforming one.
func TestExecute_RejectsAChoiceCarryingNoContent(t *testing.T) {
	g := &SchemaGuard{}
	if err := g.Init(map[string]any{"schema": objectSchema()}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newChoice(providers.Message{Content: "   "})
	if err := g.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute returned an error; a denial is a verdict, not a fault: %v", err)
	}

	if !pctx.Reject {
		t.Fatal("a response carrying no content was allowed through where an object was required")
	}
}

// A tool call is a different kind of answer, not a malformed one. A model that
// chose to call a tool returned no document for this schema to describe, and
// denying it would make the plugin incompatible with tool calling rather than
// protective of it — a gateway doing both would deny every tool-call response.
func TestExecute_AllowsAChoiceCarryingOnlyAToolCall(t *testing.T) {
	g := &SchemaGuard{}
	if err := g.Init(map[string]any{"schema": objectSchema(), "action": "block"}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newChoice(providers.Message{ToolCalls: []providers.ToolCall{{
		ID:       "call_1",
		Type:     "function",
		Function: providers.FunctionCall{Name: "lookup_city", Arguments: `{"city":"berlin"}`},
	}}})
	if err := g.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute returned an error; a denial is a verdict, not a fault: %v", err)
	}

	if pctx.Reject {
		t.Fatalf("a choice carrying only a tool call was denied: %q", pctx.Reason)
	}
}

// Every choice is validated, not only the first: n > 1 returns independent
// candidates and the caller unmarshals whichever it picks.
func TestExecute_ValidatesEveryChoice(t *testing.T) {
	g := &SchemaGuard{}
	if err := g.Init(map[string]any{"schema": objectSchema()}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := &plugin.Context{
		Stage:    plugin.StageAfterRequest,
		Metadata: map[string]any{},
		Response: &providers.Response{Choices: []providers.Choice{
			{Message: providers.Message{Content: `{"name":"ada","score":9.5}`}},
			{Message: providers.Message{Content: `{"name":"grace"}`}},
		}},
	}
	if err := g.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute returned an error; a denial is a verdict, not a fault: %v", err)
	}

	if !pctx.Reject {
		t.Fatal("a second choice missing a required field was allowed through")
	}
}

// A scalar written where a string belongs is a different fact from an absent
// key, and only one of them is a configuration. Discarding the type
// assertion's second result reads `action: 1` as "not set", so the plugin
// loads, reports itself enabled, and enforces the default the operator was
// overriding.
func TestInit_RejectsANonStringAction(t *testing.T) {
	g := &SchemaGuard{}
	err := g.Init(map[string]any{"schema": objectSchema(), "action": 1})

	if err == nil {
		t.Fatal("Init accepted a non-string action; a present-but-wrong-typed key silently takes the default")
	}
	if !strings.Contains(err.Error(), "action") {
		t.Fatalf("error does not name the key: %v", err)
	}
}

// A plugin runs inside the request pipeline, so it stops when the request is
// abandoned rather than validating documents nobody is waiting for.
//
// It returns nil, not the context's error: an error from Execute means the
// plugin BROKE, which the gateway reports as a 500 and counts against the
// target's circuit breaker. A caller hanging up is not a server fault.
func TestExecute_StopsOnACancelledContext(t *testing.T) {
	g := &SchemaGuard{}
	if err := g.Init(map[string]any{"schema": objectSchema()}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	pctx := newResponse(`{"name":"ada"}`)
	if err := g.Execute(ctx, pctx); err != nil {
		t.Fatalf("Execute returned the caller's cancellation as a plugin fault: %v", err)
	}

	if pctx.Reject {
		t.Fatal("the validation loop ran to completion on an abandoned request")
	}
}

// A malformed supported keyword must fail the load. Ignored, it produced a
// plugin that started, reported itself enabled and approved every response —
// `required: "name"` is one keystroke from the list that was meant and reads
// identically in the config.
func TestInit_RejectsAMalformedSupportedKeyword(t *testing.T) {
	for _, tc := range []struct {
		name   string
		schema map[string]any
		want   string
	}{
		{
			name:   "required written as a bare name",
			schema: map[string]any{"type": "object", "required": "name"},
			want:   "required",
		},
		{
			name:   "a required entry that is not a field name",
			schema: map[string]any{"type": "object", "required": []any{"name", 7}},
			want:   "required",
		},
		{
			name:   "type written as a list of type names",
			schema: map[string]any{"type": []any{"object", "null"}},
			want:   "type",
		},
		{
			name:   "a misspelled type name, which nothing could ever satisfy",
			schema: map[string]any{"type": "objekt"},
			want:   "objekt",
		},
		{
			name:   "properties written as a bare name",
			schema: map[string]any{"type": "object", "properties": "name"},
			want:   "properties",
		},
		{
			name:   "a property subschema that is not a schema",
			schema: map[string]any{"type": "object", "properties": map[string]any{"name": "string"}},
			want:   "name",
		},
		{
			name: "a malformed keyword nested inside a property subschema",
			schema: map[string]any{"type": "object", "properties": map[string]any{
				"user": map[string]any{"type": "object", "required": "email"},
			}},
			want: "user",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &SchemaGuard{}
			err := g.Init(map[string]any{"schema": tc.schema})

			if err == nil {
				t.Fatal("Init accepted a malformed schema keyword; the plugin loads and approves every response")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not name %q", err, tc.want)
			}
		})
	}
}

// The other half of the contract, and it must not change: a keyword this
// plugin does not implement is ignored, so a schema copied in from elsewhere
// still validates the part that is understood rather than failing the load.
func TestInit_IgnoresAnUnsupportedKeyword(t *testing.T) {
	g := &SchemaGuard{}
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"score"},
		"properties": map[string]any{
			"score": map[string]any{"type": "number", "minimum": 0, "maximum": 10},
			"name":  map[string]any{"type": "string", "pattern": "^[a-z]+$"},
		},
	}

	if err := g.Init(map[string]any{"schema": schema}); err != nil {
		t.Fatalf("Init rejected an unsupported keyword; a schema from elsewhere must still load: %v", err)
	}

	pctx := newResponse(`{"name":"ada","score":9.5}`)
	if err := g.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if pctx.Reject {
		t.Fatalf("a response conforming to the supported keywords was rejected: %q", pctx.Reason)
	}
}

func TestExecute_IgnoresBeforeRequest(t *testing.T) {
	g := &SchemaGuard{}
	if err := g.Init(map[string]any{"schema": objectSchema()}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := &plugin.Context{
		Stage:    plugin.StageBeforeRequest,
		Metadata: map[string]any{},
		Request:  &providers.Request{Messages: []providers.Message{{Content: "hello"}}},
	}
	if err := g.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute returned an error; a denial is a verdict, not a fault: %v", err)
	}

	if pctx.Reject {
		t.Fatal("schema-guard screened the request; it validates responses only")
	}
}
