package events

import (
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/models"
)

func TestHookEventMap_Size(t *testing.T) {
	event := CompletedRequest(
		"trace-123",
		"openai",
		"gpt-4o",
		25*time.Millisecond,
		false,
		10,
		5,
		models.CostResult{},
		true,
	)

	got := event.Map()
	if len(got) != 19 {
		t.Fatalf("len(Map()) = %d, want 19", len(got))
	}
}
