//go:build live
// +build live

package live_test

import (
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/gemini"
)

func TestLive_Gemini_Chat(t *testing.T) {
	apiKey := requireKey(t, "GEMINI_API_KEY")
	p, err := gemini.New(apiKey, "")
	if err != nil {
		t.Fatalf("gemini.New: %v", err)
	}
	runChatSmoke(t, p, "gemini-2.0-flash-lite")
}

func TestLive_Gemini_Stream(t *testing.T) {
	apiKey := requireKey(t, "GEMINI_API_KEY")
	p, err := gemini.New(apiKey, "")
	if err != nil {
		t.Fatalf("gemini.New: %v", err)
	}
	runStreamSmoke(t, p, "gemini-2.0-flash-lite")
}

func TestLive_Gemini_Embedding(t *testing.T) {
	apiKey := requireKey(t, "GEMINI_API_KEY")
	p, err := gemini.New(apiKey, "")
	if err != nil {
		t.Fatalf("gemini.New: %v", err)
	}
	runEmbeddingSmoke(t, p, "text-embedding-004")
}
