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

// A fine-grained personal access token carries a different prefix from the
// classic ones, and it is the format GitHub now issues by default. Matching
// only the classic prefixes let one through in both directions while the
// github_token kind reported itself selected.
func TestExecute_BlocksAFineGrainedGitHubToken(t *testing.T) {
	s := &SecretScan{}
	if err := s.Init(map[string]any{}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Structurally a fine-grained token — the prefix, the 22-character
	// identifier, the separator and the 59-character secret — and not a
	// credential: every character after the prefix is filler.
	token := "github_pat_" + strings.Repeat("A", 22) + "_" + strings.Repeat("B", 59) // #nosec G101 -- filler in the shape of a token, not a credential.
	pctx := newRequest("deploy with " + token)
	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute returned an error; a denial is a verdict, not a fault: %v", err)
	}

	if !pctx.Reject {
		t.Fatal("a fine-grained GitHub token was forwarded to the provider")
	}
	if !strings.Contains(pctx.Reason, "github_token") {
		t.Fatalf("Reason does not name the kind: %q", pctx.Reason)
	}
}

// The classic prefixes must keep matching: the fine-grained pattern is an
// addition, not a replacement.
func TestExecute_BlocksAClassicGitHubToken(t *testing.T) {
	s := &SecretScan{}
	if err := s.Init(map[string]any{}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newRequest("deploy with ghp_" + strings.Repeat("A", 36)) // #nosec G101 -- filler in the shape of a token, not a credential.
	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute returned an error; a denial is a verdict, not a fault: %v", err)
	}

	if !pctx.Reject {
		t.Fatal("a classic GitHub token was forwarded to the provider")
	}
}

// Each of these is a live credential form the curated set did not match, and
// each belongs under a kind an operator can already select. A new kind name
// would have changed what an existing `kinds` list selects; a new form under
// the existing name does not.
func TestExecute_BlocksTheAdditionalCredentialForms(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		kind    string
	}{
		{
			name:    "a Slack app-level token",
			content: "call it with xapp-1-A012345678-" + strings.Repeat("9", 12), // #nosec G101 -- filler in the shape of a token, not a credential.
			kind:    "slack_token",
		},
		{
			name:    "a Slack refresh token",
			content: "rotate with xoxe-1-" + strings.Repeat("A", 20), // #nosec G101 -- filler in the shape of a token, not a credential.
			kind:    "slack_token",
		},
		{
			name:    "a DSA private key header",
			content: "here it is: -----BEGIN DSA PRIVATE KEY-----",
			kind:    "private_key",
		},
		{
			name:    "an encrypted private key header",
			content: "here it is: -----BEGIN ENCRYPTED PRIVATE KEY-----",
			kind:    "private_key",
		},
		{
			name:    "a Stripe webhook signing secret",
			content: "verify with whsec_" + strings.Repeat("A", 32), // #nosec G101 -- filler in the shape of a secret, not a credential.
			kind:    "stripe_key",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &SecretScan{}
			if err := s.Init(map[string]any{}); err != nil {
				t.Fatalf("Init: %v", err)
			}

			pctx := newRequest(tc.content)
			if err := s.Execute(context.Background(), pctx); err != nil {
				t.Fatalf("Execute returned an error; a denial is a verdict, not a fault: %v", err)
			}

			if !pctx.Reject {
				t.Fatal("a credential was forwarded to the provider")
			}
			if !strings.Contains(pctx.Reason, tc.kind) {
				t.Fatalf("Reason %q does not name the kind %q, so an operator's kinds selection no longer covers this form", pctx.Reason, tc.kind)
			}
		})
	}
}

// The patterns match a credential's structure — a fixed prefix and a length —
// so the words around one are not evidence of anything. A false positive costs
// a developer their request.
func TestExecute_AllowsProseNamingTheAdditionalForms(t *testing.T) {
	s := &SecretScan{}
	if err := s.Init(map[string]any{}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newRequest("rotate the whsec webhook signing secret, reissue the xapp and xoxe Slack tokens, and re-encrypt the DSA private key")
	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute returned an error; a denial is a verdict, not a fault: %v", err)
	}

	if pctx.Reject {
		t.Fatalf("prose naming credential formats was blocked as a credential: %q", pctx.Reason)
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

// A plugin runs inside the request pipeline, so it stops when the request is
// abandoned rather than scanning content nobody is waiting for.
//
// It returns nil, not the context's error: an error from Execute means the
// plugin BROKE, which the gateway reports as a 500 and counts against the
// target's circuit breaker. A caller hanging up is not a server fault.
func TestExecute_StopsOnACancelledContext(t *testing.T) {
	s := &SecretScan{}
	if err := s.Init(map[string]any{}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for _, tc := range []struct {
		name string
		pctx *plugin.Context
	}{
		{name: "request", pctx: newRequest("my key is AKIAIOSFODNN7EXAMPLE")}, // #nosec G101 -- AWS's own published example key, not a live credential.
		{name: "response", pctx: &plugin.Context{
			Stage:    plugin.StageAfterRequest,
			Metadata: map[string]any{},
			Response: &providers.Response{
				Choices: []providers.Choice{{Message: providers.Message{Content: "sure: AKIAIOSFODNN7EXAMPLE"}}}, // #nosec G101 -- AWS's own published example key, not a live credential.
			},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.Execute(ctx, tc.pctx); err != nil {
				t.Fatalf("Execute returned the caller's cancellation as a plugin fault: %v", err)
			}
			if tc.pctx.Reject {
				t.Fatal("the screening loop ran to completion on an abandoned request")
			}
		})
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
