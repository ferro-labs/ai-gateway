package regexguard

import (
	"context"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

func initRegexGuard(t *testing.T, config map[string]interface{}) *RegexGuard {
	t.Helper()
	r := &RegexGuard{}
	if err := r.Init(config); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	return r
}

func regexRequest(content string) *providers.Request {
	return &providers.Request{
		Model: "gpt-4o",
		Messages: []providers.Message{
			{Role: "user", Content: content},
		},
	}
}

func regexResponse(content string) *providers.Response {
	return &providers.Response{
		ID:    "resp-1",
		Model: "gpt-4o",
		Choices: []providers.Choice{
			{Index: 0, Message: providers.Message{Role: "assistant", Content: content}, FinishReason: "stop"},
		},
	}
}

func TestRegexGuard_Init_ValidRules(t *testing.T) {
	r := initRegexGuard(t, map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{"name": "no_sql", "pattern": `(?i)drop\s+table`},
		},
	})
	if len(r.rules) != 1 {
		t.Fatalf("rules = %d, want 1", len(r.rules))
	}
}

func TestRegexGuard_Init_InvalidPattern_ReturnsError(t *testing.T) {
	r := &RegexGuard{}
	err := r.Init(map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{"name": "bad", "pattern": `(`},
		},
	})
	if err == nil {
		t.Fatal("expected invalid pattern init to fail")
	}
}

func TestRegexGuard_Init_EmptyRules(t *testing.T) {
	r := initRegexGuard(t, map[string]interface{}{"rules": []interface{}{}})
	if len(r.rules) != 0 {
		t.Fatalf("rules = %d, want 0", len(r.rules))
	}
}

func TestRegexGuard_Init_InvalidRulesType_ReturnsError(t *testing.T) {
	r := &RegexGuard{}
	err := r.Init(map[string]interface{}{
		"rules": map[string]interface{}{"name": "bad"},
	})
	if err == nil {
		t.Fatal("expected invalid rules type init to fail")
	}
	if !strings.Contains(err.Error(), "rules must be a list") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRegexGuard_BlocksMatchingInput(t *testing.T) {
	r := initRegexGuard(t, map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{"name": "no_sql", "pattern": `(?i)drop\s+table`, "action": "block"},
		},
	})
	pctx := plugin.NewContext(regexRequest("DROP TABLE users"))

	if err := r.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !pctx.Reject {
		t.Fatal("expected matching input to be blocked")
	}
}

func TestRegexGuard_AllowsNonMatchingInput(t *testing.T) {
	r := initRegexGuard(t, map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{"name": "no_sql", "pattern": `(?i)drop\s+table`, "action": "block"},
		},
	})
	pctx := plugin.NewContext(regexRequest("hello world"))

	if err := r.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if pctx.Reject {
		t.Fatal("expected non-matching input to pass")
	}
}

func TestRegexGuard_WarnMode_NoReject_MetadataSet(t *testing.T) {
	r := initRegexGuard(t, map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{"name": "warn_phone", "pattern": `\d{3}-\d{3}-\d{4}`, "action": "warn"},
		},
	})
	pctx := plugin.NewContext(regexRequest("call 555-111-2222"))

	if err := r.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if pctx.Reject {
		t.Fatal("expected warn mode not to reject")
	}
	if _, ok := pctx.Metadata["regex_rule"]; !ok {
		t.Fatal("expected regex_rule metadata")
	}
}

func TestRegexGuard_RedactActionFallsBackToBlock(t *testing.T) {
	r := initRegexGuard(t, map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{"name": "redact_not_supported", "pattern": `blocked`, "action": "redact"},
		},
	})
	pctx := plugin.NewContext(regexRequest("blocked"))

	if err := r.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !pctx.Reject {
		t.Fatal("expected unsupported redact action to fall back to blocking")
	}
}

func TestRegexGuard_ApplyToOutput_SkipsInput(t *testing.T) {
	r := initRegexGuard(t, map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{"name": "output_only", "pattern": `blocked`, "apply_to": "output", "action": "block"},
		},
	})
	pctx := plugin.NewContext(regexRequest("blocked"))

	if err := r.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if pctx.Reject {
		t.Fatal("expected output-only rule to skip input")
	}
}

func TestRegexGuard_ApplyToInput_SkipsOutput(t *testing.T) {
	r := initRegexGuard(t, map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{"name": "input_only", "pattern": `blocked`, "apply_to": "input", "action": "block"},
		},
	})
	pctx := plugin.NewContext(regexRequest("clean"))
	pctx.Response = regexResponse("blocked")

	if err := r.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if pctx.Reject {
		t.Fatal("expected input-only rule to skip output")
	}
}

func TestRegexGuard_ApplyToBoth_ScansInputAndOutput(t *testing.T) {
	r := initRegexGuard(t, map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{"name": "both", "pattern": `blocked`, "apply_to": "both", "action": "block"},
		},
	})

	pctxInput := plugin.NewContext(regexRequest("blocked"))
	if err := r.Execute(context.Background(), pctxInput); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !pctxInput.Reject {
		t.Fatal("expected apply_to=both to scan input")
	}

	pctxOutput := plugin.NewContext(regexRequest("clean"))
	pctxOutput.Response = regexResponse("blocked")
	if err := r.Execute(context.Background(), pctxOutput); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !pctxOutput.Reject {
		t.Fatal("expected apply_to=both to scan output")
	}
}

func TestRegexGuard_MultipleRules_FirstBlockShortCircuits(t *testing.T) {
	r := initRegexGuard(t, map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{"name": "first", "pattern": `blocked`, "action": "block"},
			map[string]interface{}{"name": "second", "pattern": `blocked`, "action": "block"},
		},
	})
	pctx := plugin.NewContext(regexRequest("blocked"))

	if err := r.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !pctx.Reject {
		t.Fatal("expected blocking rule to reject")
	}
	if got, _ := pctx.Metadata["regex_rule"].(string); got != "first" {
		t.Fatalf("regex_rule = %q, want first", got)
	}
}

func TestRegexGuard_WarnRuleContinuesEvaluation(t *testing.T) {
	r := initRegexGuard(t, map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{"name": "warn_first", "pattern": `blocked`, "action": "warn"},
			map[string]interface{}{"name": "block_second", "pattern": `blocked`, "action": "block"},
		},
	})
	pctx := plugin.NewContext(regexRequest("blocked"))

	if err := r.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !pctx.Reject {
		t.Fatal("expected second blocking rule to reject")
	}
	if got, _ := pctx.Metadata["regex_rule"].(string); got != "block_second" {
		t.Fatalf("regex_rule = %q, want block_second", got)
	}
}

func TestRegexGuard_NilRequest(t *testing.T) {
	r := initRegexGuard(t, map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{"name": "no_sql", "pattern": `drop\s+table`, "action": "block"},
		},
	})
	pctx := plugin.NewContext(nil)

	if err := r.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if pctx.Reject {
		t.Fatal("expected nil request not to reject")
	}
}

func TestRegexGuard_CaseInsensitivePattern(t *testing.T) {
	r := initRegexGuard(t, map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{"name": "case", "pattern": `(?i)forbidden`, "action": "block"},
		},
	})
	pctx := plugin.NewContext(regexRequest("FORBIDDEN"))

	if err := r.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !pctx.Reject {
		t.Fatal("expected case-insensitive pattern to match")
	}
}
