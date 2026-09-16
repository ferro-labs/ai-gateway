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
	_ = s.Execute(context.Background(), pctx)

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
	_ = s.Execute(context.Background(), pctx)

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
	_ = s.Execute(context.Background(), pctx)

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
	_ = s.Execute(context.Background(), pctx)

	if !pctx.Reject {
		t.Fatal("a model that echoed a credential back was not screened")
	}
}

func TestInit_ProvidersSelectsASubset(t *testing.T) {
	s := &SecretScan{}
	if err := s.Init(map[string]any{"providers": []any{"private_key"}}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newRequest("deploy with AKIAIOSFODNN7EXAMPLE please")
	_ = s.Execute(context.Background(), pctx)

	if pctx.Reject {
		t.Fatal("an AWS key was blocked although providers selected private_key only")
	}
}

func TestExecute_DeniesUninspectableContent(t *testing.T) {
	s := &SecretScan{}
	if err := s.Init(map[string]any{}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	pctx := newRequest("")
	pctx.Metadata[plugin.MetadataUninspectableContent] = true
	_ = s.Execute(context.Background(), pctx)

	if !pctx.Reject {
		t.Fatal("uninspectable content was forwarded unscanned")
	}
}
