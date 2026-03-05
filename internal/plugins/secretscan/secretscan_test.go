package secretscan

import (
	"context"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

func initSecretScan(t *testing.T, config map[string]interface{}) *SecretScan {
	t.Helper()
	s := &SecretScan{}
	if err := s.Init(config); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	return s
}

func secretRequest(content string) *providers.Request {
	return &providers.Request{
		Model: "gpt-4o",
		Messages: []providers.Message{
			{Role: "user", Content: content},
		},
	}
}

func TestSecretScan_Init_AllPatterns(t *testing.T) {
	s := initSecretScan(t, map[string]interface{}{})
	if len(s.patterns) != len(builtinSecretPatterns) {
		t.Fatalf("patterns = %d, want %d", len(s.patterns), len(builtinSecretPatterns))
	}
}

func TestSecretScan_Init_SubsetPatterns(t *testing.T) {
	s := initSecretScan(t, map[string]interface{}{
		"patterns": []interface{}{"aws_access_key", "stripe_secret"},
	})
	if len(s.patterns) != 2 {
		t.Fatalf("patterns = %d, want 2", len(s.patterns))
	}
}

func TestSecretScan_BlocksAWSKey(t *testing.T) {
	s := initSecretScan(t, map[string]interface{}{"action": "block"})
	pctx := plugin.NewContext(secretRequest("AKIA1234567890ABCD12"))

	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !pctx.Reject {
		t.Fatal("expected AWS key to be blocked")
	}
}

func TestSecretScan_BlocksOpenAIKey(t *testing.T) {
	key := "sk-" + strings.Repeat("a", 48)
	s := initSecretScan(t, map[string]interface{}{"action": "block"})
	pctx := plugin.NewContext(secretRequest("token: " + key))

	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !pctx.Reject {
		t.Fatal("expected OpenAI key to be blocked")
	}
}

func TestSecretScan_FerroStyleKeyNotMatched(t *testing.T) {
	key := "sk-ferro-" + strings.Repeat("a", 40)
	s := initSecretScan(t, map[string]interface{}{"action": "block"})
	pctx := plugin.NewContext(secretRequest("internal key: " + key))

	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if pctx.Reject {
		t.Fatal("expected non-OpenAI key format to be ignored")
	}
}

func TestSecretScan_BlocksStripeSecret(t *testing.T) {
	key := "sk_live_" + strings.Repeat("a", 24)
	s := initSecretScan(t, map[string]interface{}{"action": "block"})
	pctx := plugin.NewContext(secretRequest("stripe " + key))

	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !pctx.Reject {
		t.Fatal("expected stripe secret to be blocked")
	}
}

func TestSecretScan_BlocksGitHubPAT(t *testing.T) {
	key := "ghp_" + strings.Repeat("a", 36)
	s := initSecretScan(t, map[string]interface{}{"action": "block"})
	pctx := plugin.NewContext(secretRequest("github " + key))

	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !pctx.Reject {
		t.Fatal("expected GitHub PAT to be blocked")
	}
}

func TestSecretScan_BlocksPostgresDSN(t *testing.T) {
	s := initSecretScan(t, map[string]interface{}{"action": "block"})
	pctx := plugin.NewContext(secretRequest("postgres://user:pass@localhost:5432/db"))

	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !pctx.Reject {
		t.Fatal("expected postgres DSN to be blocked")
	}
}

func TestSecretScan_HighEntropyBlock(t *testing.T) {
	s := initSecretScan(t, map[string]interface{}{
		"action":   "block",
		"patterns": []interface{}{"high_entropy_hex"},
	})
	pctx := plugin.NewContext(secretRequest("0123456789abcdef0123456789abcdef"))

	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !pctx.Reject {
		t.Fatal("expected high entropy hex to be blocked")
	}
}

func TestSecretScan_LowEntropyHexNotBlocked(t *testing.T) {
	s := initSecretScan(t, map[string]interface{}{
		"action":   "block",
		"patterns": []interface{}{"high_entropy_hex"},
	})
	pctx := plugin.NewContext(secretRequest(strings.Repeat("a", 32)))

	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if pctx.Reject {
		t.Fatal("expected low entropy hex to be ignored")
	}
}

func TestSecretScan_WarnMode_NoReject(t *testing.T) {
	s := initSecretScan(t, map[string]interface{}{"action": "warn"})
	pctx := plugin.NewContext(secretRequest("AKIA1234567890ABCD12"))

	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if pctx.Reject {
		t.Fatal("expected warn mode not to reject")
	}
	if _, ok := pctx.Metadata["secrets_detected"]; !ok {
		t.Fatal("expected secrets_detected metadata")
	}
}

func TestSecretScan_CleanMessagePassesThrough(t *testing.T) {
	s := initSecretScan(t, map[string]interface{}{"action": "block"})
	pctx := plugin.NewContext(secretRequest("hello world"))

	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if pctx.Reject {
		t.Fatal("expected clean message to pass")
	}
}

func TestSecretScan_SkipsResponseStage(t *testing.T) {
	s := initSecretScan(t, map[string]interface{}{"action": "block"})
	pctx := plugin.NewContext(secretRequest("AKIA1234567890ABCD12"))
	pctx.Response = &providers.Response{ID: "resp", Model: "gpt-4o"}

	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if pctx.Reject {
		t.Fatal("expected response stage to be skipped")
	}
}

func TestSecretScan_NilRequest(t *testing.T) {
	s := initSecretScan(t, map[string]interface{}{"action": "block"})
	pctx := plugin.NewContext(nil)

	if err := s.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if pctx.Reject {
		t.Fatal("expected nil request not to reject")
	}
}

func TestSecretScan_ShannonEntropy_Values(t *testing.T) {
	high := shannonEntropy("0123456789abcdef0123456789abcdef")
	low := shannonEntropy(strings.Repeat("a", 32))
	if high <= low {
		t.Fatalf("expected high entropy > low entropy, got high=%f low=%f", high, low)
	}
}
