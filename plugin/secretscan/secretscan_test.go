package secretscan

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

func TestExecute_BlocksAnAWSAccessKey(t *testing.T) {
	s := &SecretScan{}
	if err := s.Init(map[string]any{}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newRequest("deploy with AKIAIOSFODNN7EXAMPLE please")
	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute returned an error; a denial is a verdict: %v", err)
	}

	if !pctx.Reject {
		t.Fatal("an AWS access key id reached the provider")
	}
}

func TestExecute_BlocksAPrivateKeyBlock(t *testing.T) {
	s := &SecretScan{}
	if err := s.Init(map[string]any{}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newRequest("here it is: -----BEGIN RSA PRIVATE KEY-----")
	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute returned an error; a denial is a verdict, not a fault: %v", err)
	}

	if !pctx.Reject {
		t.Fatal("a private key header reached the provider")
	}
}

func TestExecute_ReasonNamesTheSecretKindNotTheSecret(t *testing.T) {
	s := &SecretScan{}
	if err := s.Init(map[string]any{}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newRequest("deploy with AKIAIOSFODNN7EXAMPLE please")
	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute returned an error; a denial is a verdict, not a fault: %v", err)
	}

	if strings.Contains(pctx.Reason, "AKIAIOSFODNN7EXAMPLE") {
		t.Fatalf("Reason %q echoes the credential back to the caller and into every log between here and them", pctx.Reason)
	}
	if !strings.Contains(pctx.Reason, "aws_access_key") {
		t.Fatalf("Reason %q does not name the secret kind, so the caller cannot fix the request", pctx.Reason)
	}
}

func TestExecute_AllowsOrdinaryProse(t *testing.T) {
	s := &SecretScan{}
	if err := s.Init(map[string]any{}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newRequest("summarize our quarterly revenue report")
	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute returned an error; a denial is a verdict, not a fault: %v", err)
	}

	if pctx.Reject {
		t.Fatalf("ordinary prose was blocked as a secret: %q", pctx.Reason)
	}
}

func TestExecute_ScreensTheResponseAtAfterRequest(t *testing.T) {
	s := &SecretScan{}
	if err := s.Init(map[string]any{}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := &plugin.Context{
		Stage:    plugin.StageAfterRequest,
		Metadata: map[string]any{},
		Response: &providers.Response{
			Choices: []providers.Choice{{Message: providers.Message{Content: "sure: AKIAIOSFODNN7EXAMPLE"}}}, // #nosec G101 -- AWS's own published example key, not a live credential.
		},
	}
	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute returned an error; a denial is a verdict, not a fault: %v", err)
	}

	if !pctx.Reject {
		t.Fatal("a model that echoed a credential back was not screened")
	}
}

// A credential the model puts in a tool call's arguments leaves the gateway
// exactly as one in the message body does, and it is the field a model asked
// to "call the API with the key" fills.
func TestExecute_ScreensToolCallArgumentsInTheResponse(t *testing.T) {
	s := &SecretScan{}
	if err := s.Init(map[string]any{}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := &plugin.Context{
		Stage:    plugin.StageAfterRequest,
		Metadata: map[string]any{},
		Response: &providers.Response{
			Choices: []providers.Choice{{Message: providers.Message{
				Content: "calling the deploy tool",
				ToolCalls: []providers.ToolCall{{Function: providers.FunctionCall{
					Name:      "deploy",
					Arguments: `{"aws_key":"AKIAIOSFODNN7EXAMPLE"}`, // #nosec G101 -- AWS's own published example key, not a live credential.
				}}},
			}}},
		},
	}
	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute returned an error; a denial is a verdict, not a fault: %v", err)
	}

	if !pctx.Reject {
		t.Fatal("a credential in tool-call arguments was forwarded while the guardrail reported itself enabled")
	}
	if strings.Contains(pctx.Reason, "AKIA") {
		t.Fatalf("Reason leaked the credential: %q", pctx.Reason)
	}
}

func TestInit_KindsSelectsASubset(t *testing.T) {
	s := &SecretScan{}
	if err := s.Init(map[string]any{"kinds": []any{"private_key"}}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newRequest("deploy with AKIAIOSFODNN7EXAMPLE please")
	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute returned an error; a denial is a verdict, not a fault: %v", err)
	}

	if pctx.Reject {
		t.Fatal("an AWS key was blocked although kinds selected private_key only")
	}
}

func TestInit_RejectsAnUnknownKindName(t *testing.T) {
	s := &SecretScan{}
	err := s.Init(map[string]any{"kinds": []any{"aws-access-key"}})

	if err == nil {
		t.Fatal("Init accepted an unknown kind name; it selects nothing, so the plugin reports itself enabled and screens nothing")
	}
	if !strings.Contains(err.Error(), "aws-access-key") {
		t.Fatalf("error %q does not name the offending value", err.Error())
	}
	if !strings.Contains(err.Error(), "aws_access_key") {
		t.Fatalf("error %q does not name the accepted set", err.Error())
	}
}

func TestExecute_DeniesUninspectableContent(t *testing.T) {
	s := &SecretScan{}
	if err := s.Init(map[string]any{}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newRequest("")
	pctx.Metadata[plugin.MetadataUninspectableContent] = true
	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute returned an error; a denial is a verdict, not a fault: %v", err)
	}

	if !pctx.Reject {
		t.Fatal("uninspectable content was forwarded unscanned")
	}
}

func TestInit_RejectsAnEmptyKindsListWithNoCustomPatterns(t *testing.T) {
	s := &SecretScan{}
	// Present and empty is not the same as absent. An absent key selects every
	// curated kind; an empty list with nothing to fall back on yields a scanner
	// the catalog reports as enabled that scans for nothing.
	err := s.Init(map[string]any{"kinds": []any{}})

	if err == nil {
		t.Fatal("Init accepted an empty kinds list; it yields a guardrail that enforces nothing")
	}
}

func TestInit_EmptyKindsIsLegalAlongsideCustomPatterns(t *testing.T) {
	s := &SecretScan{}
	// "Scan for my patterns and none of the curated kinds" is a real policy, and
	// the plugin ends up with a detector, so it must load and enforce.
	if err := s.Init(map[string]any{
		"kinds":    []any{},
		"patterns": []any{`\bACME-KEY-[0-9]{8}\b`},
	}); err != nil {
		t.Fatalf("Init rejected an empty kinds list carrying a custom pattern; that config screens something: %v", err)
	}

	pctx := newRequest("the key is ACME-KEY-12345678")
	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !pctx.Reject {
		t.Fatal("a custom pattern did not screen with kinds explicitly empty")
	}
}

func TestInit_RejectsAKindsValueThatIsNotAList(t *testing.T) {
	s := &SecretScan{}
	// One name written without the list syntax. Widening it to every curated
	// kind enables patterns the operator never asked for.
	err := s.Init(map[string]any{"kinds": "aws_access_key"})

	if err == nil {
		t.Fatal("Init accepted a kinds value that is not a list; a scalar must fail the load, not silently select every kind")
	}
}

// A scalar written where a string belongs is a different fact from an absent
// key, and only one of them is a configuration. Discarding the type
// assertion's second result reads `action: 1` as "not set", so the plugin
// loads, reports itself enabled, and enforces the default the operator was
// overriding.
func TestInit_RejectsANonStringAction(t *testing.T) {
	s := &SecretScan{}
	err := s.Init(map[string]any{"action": 1})

	if err == nil {
		t.Fatal("Init accepted a non-string action; a present-but-wrong-typed key silently takes the default")
	}
	if !strings.Contains(err.Error(), "action") {
		t.Fatalf("error does not name the key: %v", err)
	}
}

func TestInit_RejectsAPatternsValueThatIsNotAList(t *testing.T) {
	s := &SecretScan{}
	// One pattern written without the list syntax. Reading it as "no custom
	// patterns" loads a plugin scanning for none of what the operator wrote.
	err := s.Init(map[string]any{"patterns": `\bACME-KEY-[0-9]{8}\b`})

	if err == nil {
		t.Fatal("Init accepted a patterns value that is not a list; the custom patterns are silently dropped")
	}
	if !strings.Contains(err.Error(), "patterns") {
		t.Fatalf("error does not name the key: %v", err)
	}
}
