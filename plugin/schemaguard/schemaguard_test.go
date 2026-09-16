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

	if pctx.Reason == "" {
		t.Fatal("Reason empty — the caller cannot tell which field was wrong")
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
