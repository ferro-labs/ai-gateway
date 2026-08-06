package core

import (
	"fmt"
	"testing"
)

func TestParseStatusCode_TypedError(t *testing.T) {
	err := &HTTPStatusError{StatusCode: 429, Message: "no parenthesised code here"}
	if got := ParseStatusCode(err); got != 429 {
		t.Errorf("ParseStatusCode = %d, want 429 (from typed error, not regex)", got)
	}
}

func TestParseStatusCode_TypedErrorWrapped(t *testing.T) {
	err := fmt.Errorf("provider x attempt 1: %w", &HTTPStatusError{StatusCode: 500, Message: "boom"})
	if got := ParseStatusCode(err); got != 500 {
		t.Errorf("ParseStatusCode = %d, want 500 (recovered through %%w wrapping)", got)
	}
}

// TestParseStatusCode_NeverScrapesTheMessage pins the deletion of the regex
// fallback. A parenthesised code in an untyped error's text used to yield that
// status, which made an error that merely LOOKED classified indistinguishable
// from one that was — and, far worse, made the reverse silently true for every
// SDK-backed call, whose message has no parentheses and so scraped to 0. 0 means
// "transport error" downstream. Status comes from the type or not at all.
func TestParseStatusCode_NeverScrapesTheMessage(t *testing.T) {
	for _, msg := range []string{
		"legacy provider error (418): teapot",
		`POST "https://api.example.com/v1/embeddings": 429 Too Many Requests`,
		"all providers failed: provider openai attempt 3: (503)",
	} {
		if got := ParseStatusCode(fmt.Errorf("%s", msg)); got != 0 {
			t.Errorf("ParseStatusCode(%q) = %d, want 0 — the message is not a status source", msg, got)
		}
	}
}

func TestParseStatusCode_NoCode(t *testing.T) {
	if got := ParseStatusCode(fmt.Errorf("no code here")); got != 0 {
		t.Errorf("ParseStatusCode = %d, want 0", got)
	}
}

func TestParseStatusCode_Nil(t *testing.T) {
	if got := ParseStatusCode(nil); got != 0 {
		t.Errorf("ParseStatusCode = %d, want 0", got)
	}
}
