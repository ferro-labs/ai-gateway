package core

import "testing"

func TestNormalizeFinishReason(t *testing.T) {
	cases := []struct {
		name   string
		native string
		want   string
	}{
		// Anthropic (direct + Bedrock Claude)
		{"anthropic end_turn", "end_turn", "stop"},
		{"anthropic stop_sequence", "stop_sequence", "stop"},
		{"anthropic max_tokens", "max_tokens", "length"},
		{"anthropic tool_use", "tool_use", "tool_calls"},
		{"anthropic refusal", "refusal", "content_filter"},

		// Bedrock Titan (uppercase completionReason)
		{"titan FINISH", "FINISH", "stop"},
		{"titan LENGTH", "LENGTH", "length"},
		{"titan CONTENT_FILTERED", "CONTENT_FILTERED", "content_filter"},

		// Bedrock Llama (already lowercase-ish)
		{"llama stop", "stop", "stop"},
		{"llama length", "length", "length"},

		// Cohere (uppercase finish_reason)
		{"cohere COMPLETE", "COMPLETE", "stop"},
		{"cohere MAX_TOKENS", "MAX_TOKENS", "length"},
		{"cohere STOP_SEQUENCE", "STOP_SEQUENCE", "stop"},
		{"cohere TOOL_CALL", "TOOL_CALL", "tool_calls"},
		{"cohere ERROR_TOXIC", "ERROR_TOXIC", "content_filter"},

		// Already-canonical passthrough
		{"canonical tool_calls", "tool_calls", "tool_calls"},
		{"canonical content_filter", "content_filter", "content_filter"},

		// Edge cases
		{"empty stays empty", "", ""},
		{"whitespace trimmed", "  end_turn  ", "stop"},
		{"unknown passthrough", "some_new_reason", "some_new_reason"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeFinishReason(tc.native); got != tc.want {
				t.Errorf("NormalizeFinishReason(%q) = %q, want %q", tc.native, got, tc.want)
			}
		})
	}
}
