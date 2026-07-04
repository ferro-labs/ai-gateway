package replicate

import "testing"

// TestNewReplicate_RejectsInvalidBaseURL locks in base-URL validation.
func TestNewReplicate_RejectsInvalidBaseURL(t *testing.T) {
	if _, err := New("k", "://nope", nil, nil); err == nil {
		t.Fatal("New accepted an invalid base URL")
	}
}

// TestResolveModelURL_EscapesModelPath locks in that special characters in a
// model path are percent-escaped so they cannot alter the request URL.
func TestResolveModelURL_EscapesModelPath(t *testing.T) {
	p, err := New("k", "https://api.replicate.com/v1", nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, _ := p.resolveModelURL("owner/bad#name")
	want := "https://api.replicate.com/v1/models/owner/bad%23name/predictions"
	if got != want {
		t.Errorf("resolveModelURL = %q, want %q", got, want)
	}
}
