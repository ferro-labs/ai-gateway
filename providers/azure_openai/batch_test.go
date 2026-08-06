package azureopenai

import (
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestAzureOpenAIProvider_Batch_Interface(_ *testing.T) {
	p, _ := New("k", "https://res.openai.azure.com", "dep", "")
	var _ core.BatchProvider = p
}

// Azure serves Files/Batch on the resource's /openai/v1 root — a different root
// than the deployment path its chat surface uses — and authenticates with the
// api-key header, not a bearer token.
func TestAzureOpenAIProvider_BatchBaseURLAndAuth(t *testing.T) {
	p, _ := New("secret-key", "https://res.openai.azure.com/", "dep", "")

	if got := p.BatchBaseURL(); got != "https://res.openai.azure.com/openai/v1" {
		t.Errorf("BatchBaseURL() = %q, want https://res.openai.azure.com/openai/v1", got)
	}
	auth := p.BatchAuthHeaders()
	if auth["api-key"] != "secret-key" {
		t.Errorf("BatchAuthHeaders api-key = %q, want secret-key", auth["api-key"])
	}
	if _, hasBearer := auth["Authorization"]; hasBearer {
		t.Errorf("BatchAuthHeaders must not set Authorization; Azure uses api-key")
	}
}
