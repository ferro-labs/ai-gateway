package qwen

import "testing"

// TestNewQwen_RejectsInvalidBaseURL locks in the base-URL validation.
func TestNewQwen_RejectsInvalidBaseURL(t *testing.T) {
	if _, err := New("k", "://bad"); err == nil {
		t.Fatal("New accepted an invalid base URL")
	}
}
