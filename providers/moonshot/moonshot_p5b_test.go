package moonshot

import "testing"

// TestNewMoonshot_RejectsInvalidBaseURL locks in the base-URL validation.
func TestNewMoonshot_RejectsInvalidBaseURL(t *testing.T) {
	if _, err := New("k", "://bad"); err == nil {
		t.Fatal("New accepted an invalid base URL")
	}
}
