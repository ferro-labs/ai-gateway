//go:build live
// +build live

package live_test

import (
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/anthropic"
)

func TestLive_Anthropic_Chat(t *testing.T) {
	apiKey := requireKey(t, "ANTHROPIC_API_KEY")
	p, err := anthropic.New(apiKey, "")
	if err != nil {
		t.Fatalf("anthropic.New: %v", err)
	}
	runChatSmoke(t, p, "claude-3-haiku-20240307")
}

func TestLive_Anthropic_Stream(t *testing.T) {
	apiKey := requireKey(t, "ANTHROPIC_API_KEY")
	p, err := anthropic.New(apiKey, "")
	if err != nil {
		t.Fatalf("anthropic.New: %v", err)
	}
	runStreamSmoke(t, p, "claude-3-haiku-20240307")
}
