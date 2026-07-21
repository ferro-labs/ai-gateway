package redact

import (
	"errors"
	"strings"
	"testing"
)

// fakeOpenAIKey has the legacy sk- + 20-or-more-alphanumerics shape the
// openai_key policy matches. It is not a real credential.
var fakeOpenAIKey = buildKey("sk-", "abc123DEF456ghi789JKL012mno345")

func TestString_RedactsKnownCredentialShapes(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		absent  string
		present string
	}{
		{
			name:    "openai_key",
			in:      "invalid api key: " + fakeOpenAIKey,
			absent:  fakeOpenAIKey,
			present: "[REDACTED_OPENAI_KEY]",
		},
		{
			name:    "bearer_token",
			in:      "unauthorized for Bearer abc123DEF456ghi789",
			absent:  "abc123DEF456ghi789",
			present: "[REDACTED_BEARER_TOKEN]",
		},
		{
			name:    "email",
			in:      "no account for someone@example.com",
			absent:  "someone@example.com",
			present: "[REDACTED_EMAIL]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := String(tt.in)
			if strings.Contains(got, tt.absent) {
				t.Errorf("String() leaked %q: %q", tt.absent, got)
			}
			if !strings.Contains(got, tt.present) {
				t.Errorf("String() = %q, want it to contain %q", got, tt.present)
			}
		})
	}
}

func TestString_LeavesOrdinaryTextUnchanged(t *testing.T) {
	const in = "openai API error (503): upstream is unavailable"
	if got := String(in); got != in {
		t.Errorf("String(%q) = %q, want it unchanged", in, got)
	}
}

func TestErrorMessage_NilReturnsEmpty(t *testing.T) {
	if got := ErrorMessage(nil); got != "" {
		t.Errorf("ErrorMessage(nil) = %q, want %q", got, "")
	}
}

func TestErrorMessage_RedactsErrorText(t *testing.T) {
	err := errors.New("openai API error (401): bad key " + fakeOpenAIKey)
	got := ErrorMessage(err)
	if strings.Contains(got, fakeOpenAIKey) {
		t.Fatalf("ErrorMessage() leaked the key: %q", got)
	}
	if !strings.Contains(got, "[REDACTED_OPENAI_KEY]") {
		t.Errorf("ErrorMessage() = %q, want a redaction token", got)
	}
}

// Applying the policies to already-redacted text must be a no-op, since the
// sinks that redact centrally may receive text a caller already filtered.
func TestString_IsStableOnAlreadyRedactedText(t *testing.T) {
	once := String("key " + fakeOpenAIKey + " for someone@example.com via Bearer abc123DEF456ghi789")
	if twice := String(once); twice != once {
		t.Errorf("second pass changed the text:\n first: %q\nsecond: %q", once, twice)
	}
}
