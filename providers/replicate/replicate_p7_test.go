package replicate

import "testing"

// TestNewReplicate_RejectsInvalidBaseURL locks in base-URL validation.
func TestNewReplicate_RejectsInvalidBaseURL(t *testing.T) {
	if _, err := New("k", "://nope", nil, nil); err == nil {
		t.Fatal("New accepted an invalid base URL")
	}
}
