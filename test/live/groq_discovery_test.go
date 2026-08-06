//go:build live

package live_test

import (
	"context"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/providers/groq"
)

func TestLive_Groq_Discovery(t *testing.T) {
	key := requireKey(t, "GROQ_API_KEY")

	p, err := groq.New(key, "")
	if err != nil {
		t.Fatalf("groq.New error = %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	got, err := p.DiscoverModels(ctx)
	if err != nil {
		t.Fatalf("DiscoverModels() error = %T", err)
	}

	target := groqChat.id
	found := false
	for _, model := range got {
		if model.ID == target {
			found = true
			break
		}
	}
	t.Logf("discovered_model_count=%d target_present=%t", len(got), found)
	if !found {
		t.Fatalf("approved live model is absent from discovery")
	}
}
