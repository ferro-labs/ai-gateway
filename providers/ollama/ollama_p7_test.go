package ollama

import "testing"

// TestNewOllama_RejectsInvalidBaseURL locks in base-URL validation.
func TestNewOllama_RejectsInvalidBaseURL(t *testing.T) {
	if _, err := New("://nope", nil); err == nil {
		t.Fatal("New accepted an invalid base URL")
	}
}
