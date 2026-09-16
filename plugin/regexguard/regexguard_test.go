package regexguard

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

func TestExecute_BlocksOnMatchingPattern(t *testing.T) {
	g := &RegexGuard{}
	if err := g.Init(map[string]any{
		"rules": []any{map[string]any{"name": "ssn", "pattern": `\d{3}-\d{2}-\d{4}`, "action": "block"}},
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newRequest("my ssn is 123-45-6789")
	if err := g.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute returned an error; a denial is a verdict, not a fault: %v", err)
	}

	if !pctx.Reject {
		t.Fatal("a request matching a block rule was not rejected")
	}
}

func TestExecute_ReasonDoesNotLeakThePattern(t *testing.T) {
	g := &RegexGuard{}
	if err := g.Init(map[string]any{
		"rules": []any{map[string]any{"name": "ssn", "pattern": `\d{3}-\d{2}-\d{4}`, "action": "block"}},
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newRequest("my ssn is 123-45-6789")
	if err := g.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute returned an error; a denial is a verdict, not a fault: %v", err)
	}

	// Without this the test passes on a plugin that never matched at all: an
	// empty reason leaks nothing, so the assertion below holds for a guardrail
	// that enforced nothing.
	if !pctx.Reject {
		t.Fatal("the rule did not fire, so the reason under test was never produced")
	}
	for _, leak := range []string{`\d{3}`, "123-45-6789"} {
		if strings.Contains(pctx.Reason, leak) {
			t.Fatalf("Reason %q leaks %q — one probe at a time reconstructs the operator's policy", pctx.Reason, leak)
		}
	}
}

func TestExecute_NonBlockingActionAllowsTheRequest(t *testing.T) {
	g := &RegexGuard{}
	if err := g.Init(map[string]any{
		"rules": []any{map[string]any{"name": "ssn", "pattern": `\d{3}-\d{2}-\d{4}`, "action": "warn"}},
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newRequest("my ssn is 123-45-6789")
	if err := g.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute returned an error; a denial is a verdict, not a fault: %v", err)
	}

	if pctx.Reject {
		t.Fatal("action \"warn\" rejected the request — an observe-only rollout must not block")
	}
}

func TestExecute_OutputScopedRuleDoesNotScreenTheRequest(t *testing.T) {
	g := &RegexGuard{}
	if err := g.Init(map[string]any{
		"rules": []any{map[string]any{"name": "codename", "pattern": "bluebird", "apply_to": "output", "action": "block"}},
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newRequest("tell me about bluebird")
	if err := g.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute returned an error; a denial is a verdict, not a fault: %v", err)
	}

	if pctx.Reject {
		t.Fatal("an output-scoped rule screened the request")
	}
}

func TestExecute_OutputScopedRuleScreensTheResponse(t *testing.T) {
	g := &RegexGuard{}
	if err := g.Init(map[string]any{
		"rules": []any{map[string]any{"name": "codename", "pattern": "bluebird", "apply_to": "output", "action": "block"}},
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := &plugin.Context{
		Stage:    plugin.StageAfterRequest,
		Metadata: map[string]any{},
		Response: &providers.Response{
			Choices: []providers.Choice{{Message: providers.Message{Content: "project bluebird is..."}}},
		},
	}
	if err := g.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute returned an error; a denial is a verdict, not a fault: %v", err)
	}

	if !pctx.Reject {
		t.Fatal("an output-scoped rule did not screen the response")
	}
}

func TestInit_RejectsAnUncompilablePattern(t *testing.T) {
	g := &RegexGuard{}
	err := g.Init(map[string]any{
		"rules": []any{map[string]any{"name": "bad", "pattern": "([unclosed"}},
	})

	if err == nil {
		t.Fatal("Init accepted an uncompilable pattern; it must fail at load, not silently match nothing forever")
	}
}

func TestExecute_DeniesUninspectableContent(t *testing.T) {
	g := &RegexGuard{}
	if err := g.Init(map[string]any{
		"rules": []any{map[string]any{"name": "ssn", "pattern": `\d{3}-\d{2}-\d{4}`}},
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newRequest("")
	pctx.Metadata[plugin.MetadataUninspectableContent] = true
	if err := g.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute returned an error for uninspectable content; it must be a verdict: %v", err)
	}

	if !pctx.Reject {
		t.Fatal("uninspectable content was forwarded unscreened — the policy is evadable by one tokenizer call")
	}
}

func TestInit_RejectsAnUnrecognisedApplyTo(t *testing.T) {
	g := &RegexGuard{}
	// "outupt" reads as an operator asking to screen the model's answer.
	// Mapping it to input screens the prompt instead, which is a different
	// rule, silently substituted.
	err := g.Init(map[string]any{
		"rules": []any{map[string]any{"name": "codename", "pattern": "bluebird", "apply_to": "outupt"}},
	})

	if err == nil {
		t.Fatal("Init accepted an unrecognized apply_to; input screening is not a superset of output screening, so the rule the operator wrote never runs")
	}
	if !strings.Contains(err.Error(), "outupt") {
		t.Fatalf("error %q does not name the offending value", err.Error())
	}
	for _, want := range []string{"input", "output", "both"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not name the accepted set (missing %q)", err.Error(), want)
		}
	}
}

func TestInit_AbsentApplyToDefaultsToInputWithoutError(t *testing.T) {
	g := &RegexGuard{}
	if err := g.Init(map[string]any{
		"rules": []any{map[string]any{"name": "ssn", "pattern": `\d{3}-\d{2}-\d{4}`}},
	}); err != nil {
		t.Fatalf("Init rejected a rule that omits apply_to; an absent scope is not a misspelling: %v", err)
	}

	pctx := newRequest("my ssn is 123-45-6789")
	if err := g.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute returned an error; a denial is a verdict, not a fault: %v", err)
	}

	if !pctx.Reject {
		t.Fatal("a rule omitting apply_to did not screen the request")
	}
}

func TestInit_RejectsARulesBlockThatIsNotAList(t *testing.T) {
	g := &RegexGuard{}
	// The shape a rules block written as a mapping decodes to. Reading it as
	// "no rules" yields a plugin the catalog reports as enabled and that
	// screens nothing.
	err := g.Init(map[string]any{
		"rules": map[string]any{"name": "ssn", "pattern": `\d{3}-\d{2}-\d{4}`},
	})

	if err == nil {
		t.Fatal("Init accepted a rules block that is not a list; it yields zero rules and a guardrail that enforces nothing")
	}
}

func TestInit_AbsentRulesIsANoOpWithoutError(t *testing.T) {
	g := &RegexGuard{}
	if err := g.Init(map[string]any{"action": "warn"}); err != nil {
		t.Fatalf("Init rejected a config carrying no rules key; an absent key is not a misconfiguration: %v", err)
	}

	pctx := newRequest("my ssn is 123-45-6789")
	if err := g.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if pctx.Reject {
		t.Fatal("a plugin with no rules rejected a request")
	}
}

func TestInit_RejectsAnUnrecognisedTopLevelAction(t *testing.T) {
	g := &RegexGuard{}
	err := g.Init(map[string]any{
		"action": "blockk",
		"rules":  []any{map[string]any{"name": "ssn", "pattern": `\d{3}-\d{2}-\d{4}`}},
	})

	if err == nil {
		t.Fatal("Init accepted an unrecognized top-level action; a misspelling must fail the load, not silently stop enforcing")
	}
}

func TestInit_RejectsAnUnrecognisedRuleAction(t *testing.T) {
	g := &RegexGuard{}
	err := g.Init(map[string]any{
		"rules": []any{map[string]any{"name": "ssn", "pattern": `\d{3}-\d{2}-\d{4}`, "action": "blockk"}},
	})

	if err == nil {
		t.Fatal("Init accepted an unrecognized rule action; a misspelling must fail the load, not silently stop enforcing")
	}
}

func TestInit_RejectsAnEmptyRulesList(t *testing.T) {
	g := &RegexGuard{}
	// Present and empty is not the same as absent. An absent key means the
	// plugin was not configured; an empty list is an operator who meant to name
	// rules, and loading it yields a guardrail the catalog reports as enabled
	// that screens nothing.
	err := g.Init(map[string]any{"rules": []any{}})

	if err == nil {
		t.Fatal("Init accepted an empty rules list; it yields a guardrail that enforces nothing")
	}
}

// A scalar written where a string belongs is a different fact from an absent
// key, and only one of them is a configuration. Discarding the type
// assertion's second result reads `action: 1` as "not set", so the plugin
// loads, reports itself enabled, and enforces the default the operator was
// overriding.
func TestInit_RejectsANonStringTopLevelAction(t *testing.T) {
	g := &RegexGuard{}
	err := g.Init(map[string]any{
		"action": 1,
		"rules":  []any{map[string]any{"name": "ssn", "pattern": `\d{3}-\d{2}-\d{4}`}},
	})

	if err == nil {
		t.Fatal("Init accepted a non-string action; a present-but-wrong-typed key silently takes the default")
	}
	if !strings.Contains(err.Error(), "action") {
		t.Fatalf("error does not name the key: %v", err)
	}
}

func TestInit_RejectsANonStringRuleAction(t *testing.T) {
	g := &RegexGuard{}
	err := g.Init(map[string]any{
		"rules": []any{map[string]any{"name": "ssn", "pattern": `\d{3}-\d{2}-\d{4}`, "action": 1}},
	})

	if err == nil {
		t.Fatal("Init accepted a non-string rule action; a present-but-wrong-typed key silently takes the default")
	}
	if !strings.Contains(err.Error(), "action") {
		t.Fatalf("error does not name the key: %v", err)
	}
}

// apply_to is the one whose silent default is a DIFFERENT rule rather than a
// weaker one: an operator writing a scalar while meaning to screen the model's
// answer gets a rule that screens the prompt instead.
func TestInit_RejectsANonStringApplyTo(t *testing.T) {
	g := &RegexGuard{}
	err := g.Init(map[string]any{
		"rules": []any{map[string]any{"name": "ssn", "pattern": `\d{3}-\d{2}-\d{4}`, "apply_to": 5}},
	})

	if err == nil {
		t.Fatal("Init accepted a non-string apply_to; it becomes a silent input-only rule")
	}
	if !strings.Contains(err.Error(), "apply_to") {
		t.Fatalf("error does not name the key: %v", err)
	}
}

func TestInit_RejectsANonStringRuleName(t *testing.T) {
	g := &RegexGuard{}
	err := g.Init(map[string]any{
		"rules": []any{map[string]any{"name": 7, "pattern": `\d{3}-\d{2}-\d{4}`}},
	})

	if err == nil {
		t.Fatal("Init accepted a non-string rule name; the rule silently logs under a generated one")
	}
	if !strings.Contains(err.Error(), "name") {
		t.Fatalf("error does not name the key: %v", err)
	}
}

func TestInit_RejectsANonStringPattern(t *testing.T) {
	g := &RegexGuard{}
	err := g.Init(map[string]any{
		"rules": []any{map[string]any{"name": "ssn", "pattern": 1234}},
	})

	if err == nil {
		t.Fatal("Init accepted a non-string pattern")
	}
	if !strings.Contains(err.Error(), "pattern") {
		t.Fatalf("error does not name the key: %v", err)
	}
}
