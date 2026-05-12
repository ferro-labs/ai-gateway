//go:build live
// +build live

package live_test

import (
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/groq"
)

func TestLive_Groq_Chat(t *testing.T) {
	apiKey := requireKey(t, "GROQ_API_KEY")
	p, err := groq.New(apiKey, "")
	if err != nil {
		t.Fatalf("groq.New: %v", err)
	}
	runChatSmoke(t, p, "llama-3.1-8b-instant")
}

func TestLive_Groq_Stream(t *testing.T) {
	apiKey := requireKey(t, "GROQ_API_KEY")
	p, err := groq.New(apiKey, "")
	if err != nil {
		t.Fatalf("groq.New: %v", err)
	}
	runStreamSmoke(t, p, "llama-3.1-8b-instant")
}
