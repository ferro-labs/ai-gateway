package core

import (
	"strings"
	"testing"
)

// TestAPIError_Envelopes verifies APIError extracts the message from the OpenAI
// envelope and the FastAPI {"detail":…} envelope, and falls back to the raw body.
func TestAPIError_Envelopes(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"openai", `{"error":{"message":"bad key"}}`, "bad key"},
		{"detail", `{"detail":"Invalid API Key"}`, "Invalid API Key"},
		{"raw fallback", `weird body`, "weird body"},
		{"openai wins over detail", `{"error":{"message":"m"},"detail":"d"}`, "m"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := APIError("test", 401, []byte(tc.body))
			if err == nil {
				t.Fatal("APIError returned nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tc.want)
			}
			if !strings.Contains(err.Error(), "(401)") {
				t.Errorf("error = %q, want it to embed the status", err.Error())
			}
		})
	}
}
