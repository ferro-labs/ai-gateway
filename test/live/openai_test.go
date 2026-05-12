//go:build live
// +build live

package live_test

import (
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/openai"
)

func TestLive_OpenAI_Chat(t *testing.T) {
	apiKey := requireKey(t, "OPENAI_API_KEY")
	p, err := openai.New(apiKey, "")
	if err != nil {
		t.Fatalf("openai.New: %v", err)
	}
	runChatSmoke(t, p, "gpt-4o-mini")
}

func TestLive_OpenAI_Stream(t *testing.T) {
	apiKey := requireKey(t, "OPENAI_API_KEY")
	p, err := openai.New(apiKey, "")
	if err != nil {
		t.Fatalf("openai.New: %v", err)
	}
	runStreamSmoke(t, p, "gpt-4o-mini")
}

func TestLive_OpenAI_Embedding(t *testing.T) {
	apiKey := requireKey(t, "OPENAI_API_KEY")
	p, err := openai.New(apiKey, "")
	if err != nil {
		t.Fatalf("openai.New: %v", err)
	}
	runEmbeddingSmoke(t, p, "text-embedding-3-small")
}
