package schemaguard

import (
	"context"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

func initSchemaGuard(t *testing.T, config map[string]interface{}) *SchemaGuard {
	t.Helper()
	s := &SchemaGuard{}
	if err := s.Init(config); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	return s
}

func baseSchemaConfig() map[string]interface{} {
	return map[string]interface{}{
		"schema": map[string]interface{}{
			"type":     "object",
			"required": []interface{}{"name", "confidence"},
			"properties": map[string]interface{}{
				"name": map[string]interface{}{"type": "string"},
				"confidence": map[string]interface{}{
					"type":    "number",
					"minimum": 0,
					"maximum": 1,
				},
			},
		},
	}
}

func responseContext(content string) *plugin.Context {
	pctx := plugin.NewContext(&providers.Request{Model: "gpt-4o"})
	pctx.Response = &providers.Response{
		ID:    "resp-1",
		Model: "gpt-4o",
		Choices: []providers.Choice{
			{Index: 0, Message: providers.Message{Role: "assistant", Content: content}, FinishReason: "stop"},
		},
	}
	return pctx
}

func requestContext(content string) *plugin.Context {
	return plugin.NewContext(&providers.Request{
		Model: "gpt-4o",
		Messages: []providers.Message{
			{Role: "user", Content: content},
		},
	})
}

func TestSchemaGuard_Init_ValidSchema(t *testing.T) {
	_ = initSchemaGuard(t, baseSchemaConfig())
}

func TestSchemaGuard_Init_InvalidSchema_ReturnsError(t *testing.T) {
	s := &SchemaGuard{}
	config := map[string]interface{}{
		"schema": map[string]interface{}{
			"type": "not-a-valid-type",
		},
	}
	if err := s.Init(config); err == nil {
		t.Fatal("expected invalid schema init to fail")
	}
}

func TestSchemaGuard_Init_MissingSchema_ReturnsError(t *testing.T) {
	s := &SchemaGuard{}
	if err := s.Init(map[string]interface{}{}); err == nil {
		t.Fatal("expected missing schema init to fail")
	}
}

func TestSchemaGuard_ValidOutput_PassesThrough(t *testing.T) {
	s := initSchemaGuard(t, baseSchemaConfig())
	pctx := responseContext(`{"name":"ok","confidence":0.9}`)

	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if pctx.Reject {
		t.Fatal("expected valid output to pass")
	}
}

func TestSchemaGuard_InvalidOutput_Blocks(t *testing.T) {
	s := initSchemaGuard(t, baseSchemaConfig())
	pctx := responseContext("not-json")

	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !pctx.Reject {
		t.Fatal("expected invalid output to block")
	}
}

func TestSchemaGuard_MissingRequiredField_Blocks(t *testing.T) {
	s := initSchemaGuard(t, baseSchemaConfig())
	pctx := responseContext(`{"name":"ok"}`)

	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !pctx.Reject {
		t.Fatal("expected missing required field to block")
	}
}

func TestSchemaGuard_WrongType_Blocks(t *testing.T) {
	s := initSchemaGuard(t, baseSchemaConfig())
	pctx := responseContext(`{"name":"ok","confidence":"high"}`)

	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !pctx.Reject {
		t.Fatal("expected wrong type to block")
	}
}

func TestSchemaGuard_WarnMode_NoReject_MetadataSet(t *testing.T) {
	config := baseSchemaConfig()
	config["action"] = "warn"
	s := initSchemaGuard(t, config)
	pctx := responseContext("not-json")

	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if pctx.Reject {
		t.Fatal("expected warn mode not to reject")
	}
	if _, ok := pctx.Metadata["schema_violation"]; !ok {
		t.Fatal("expected schema_violation metadata in warn mode")
	}
}

func TestSchemaGuard_ExtractJSON_StripsFences(t *testing.T) {
	content := "```json\n{\"name\":\"ok\"}\n```"
	if got := extractJSON(content); got != `{"name":"ok"}` {
		t.Fatalf("extractJSON() = %q, want %q", got, `{"name":"ok"}`)
	}
}

func TestSchemaGuard_ExtractJSON_NoFence_UsesRawContent(t *testing.T) {
	content := `{"name":"ok"}`
	if got := extractJSON(content); got != content {
		t.Fatalf("extractJSON() = %q, want %q", got, content)
	}
}

func TestSchemaGuard_ApplyToInput_ValidatesRequest(t *testing.T) {
	config := baseSchemaConfig()
	config["apply_to"] = schemaGuardApplyToInput
	s := initSchemaGuard(t, config)
	pctx := requestContext(`{"name":"ok","confidence":0.9}`)

	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if pctx.Reject {
		t.Fatal("expected valid input JSON to pass")
	}

	pctx2 := requestContext(`{"name":"ok","confidence":"high"}`)
	if err := s.Execute(context.Background(), pctx2); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !pctx2.Reject {
		t.Fatal("expected invalid input JSON to be rejected")
	}
}

func TestSchemaGuard_ApplyToInput_NonJSON_Blocks(t *testing.T) {
	config := baseSchemaConfig()
	config["apply_to"] = schemaGuardApplyToInput
	s := initSchemaGuard(t, config)
	pctx := requestContext("not-json")

	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !pctx.Reject {
		t.Fatal("expected non-JSON input to be rejected")
	}
	violation, _ := pctx.Metadata["schema_violation"].(string)
	if !strings.HasPrefix(violation, "input failed schema validation:") {
		t.Fatalf("schema_violation = %q, expected input validation failure prefix", violation)
	}
}

func TestSchemaGuard_ApplyToInput_NonJSON_WarnSetsMetadata(t *testing.T) {
	config := baseSchemaConfig()
	config["apply_to"] = schemaGuardApplyToInput
	config["action"] = schemaGuardActionWarn
	s := initSchemaGuard(t, config)
	pctx := requestContext("not-json")

	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if pctx.Reject {
		t.Fatal("expected warn mode not to reject non-JSON input")
	}
	if _, ok := pctx.Metadata["schema_violation"]; !ok {
		t.Fatal("expected schema_violation metadata for non-JSON input")
	}
}

func TestSchemaGuard_ApplyToOutput_SkipsRequest(t *testing.T) {
	s := initSchemaGuard(t, baseSchemaConfig())
	pctx := requestContext("this is not json")

	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if pctx.Reject {
		t.Fatal("expected output-only mode to skip request validation")
	}
}

func TestSchemaGuard_NilRequest(t *testing.T) {
	config := baseSchemaConfig()
	config["apply_to"] = schemaGuardApplyToInput
	s := initSchemaGuard(t, config)
	pctx := plugin.NewContext(nil)

	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if pctx.Reject {
		t.Fatal("expected nil request not to reject")
	}
}

func TestSchemaGuard_NonJSONResponse_OnInput_Skipped(t *testing.T) {
	config := baseSchemaConfig()
	config["apply_to"] = schemaGuardApplyToInput
	s := initSchemaGuard(t, config)
	pctx := responseContext("not-json")

	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if pctx.Reject {
		t.Fatal("expected input-only mode to skip response validation")
	}
	if violation, ok := pctx.Metadata["schema_violation"].(string); ok && strings.TrimSpace(violation) != "" {
		t.Fatalf("unexpected schema_violation metadata: %q", violation)
	}
}
