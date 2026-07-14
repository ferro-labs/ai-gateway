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

func TestParseStatusCode_RegexFallback(t *testing.T) {
	err := fmt.Errorf("legacy provider error (418): teapot")
	if got := ParseStatusCode(err); got != 418 {
		t.Errorf("ParseStatusCode = %d, want 418 (regex fallback for non-typed errors)", got)
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
