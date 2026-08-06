package ai21

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// TestCompleteStream_NonJambaModelCarries400 pins the classification of the
// refusal, not just its existence. Only Jamba models stream here, and the caller
// named something else — a 400 they can fix, not the 500 a bare error classified
// as, and not a status-less error that strategies.shouldRetry reads as a
// transport failure and retries until the budget is gone.
func TestCompleteStream_NonJambaModelCarries400(t *testing.T) {
	p, err := New("test-key", "")
	if err != nil {
		t.Fatalf("New(): %v", err)
	}

	_, err = p.CompleteStream(context.Background(), core.Request{
		Model:    "j2-ultra",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("CompleteStream() with a Jurassic model returned no error")
	}
	if got := core.ParseStatusCode(err); got != http.StatusBadRequest {
		t.Errorf("ParseStatusCode(%v) = %d, want %d", err, got, http.StatusBadRequest)
	}
	if !strings.Contains(err.Error(), "Jamba") {
		t.Errorf("error = %q, want it to name the model family that does stream", err.Error())
	}
}
