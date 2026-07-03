package together

import "testing"

// TestNewTogether_DefaultDomain verifies the zero-value base URL resolves to the
// current api.together.ai host (migrated from the legacy .xyz domain).
func TestNewTogether_DefaultDomain(t *testing.T) {
	p, err := New("test-key", "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := p.BaseURL(); got != "https://api.together.ai" {
		t.Errorf("default BaseURL() = %q, want https://api.together.ai", got)
	}
}

// TestNewTogether_RejectsInvalidBaseURL locks in the shared base-URL validation.
func TestNewTogether_RejectsInvalidBaseURL(t *testing.T) {
	if _, err := New("k", "://bad"); err == nil {
		t.Fatal("New accepted an invalid base URL")
	}
}
