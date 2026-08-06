package core

import "strings"

// OpenAI-canonical finish_reason values. Every provider normalizes its native
// stop reason to one of these so clients can uniformly detect truncation
// (length) and tool use (tool_calls) regardless of the upstream provider.
const (
	FinishReasonStop          = "stop"
	FinishReasonLength        = "length"
	FinishReasonToolCalls     = "tool_calls"
	FinishReasonContentFilter = "content_filter"
)

// NormalizeFinishReason maps a provider's native stop reason to the
// OpenAI-canonical finish_reason vocabulary (stop | length | tool_calls |
// content_filter). Matching is case-insensitive so the uppercase conventions
// used by Cohere, Bedrock Titan, and Gemini are handled alongside the lowercase
// Anthropic and Llama forms.
//
// An empty input returns empty (a non-final stream chunk carries no reason).
// An unrecognized value is returned unchanged so new upstream reasons are
// surfaced rather than silently rewritten to "stop"; Gemini's ambiguous
// terminal reasons (OTHER, MALFORMED_FUNCTION_CALL, UNEXPECTED_TOOL_CALL) are
// intentionally passed through rather than coerced.
func NormalizeFinishReason(native string) string {
	switch strings.ToLower(strings.TrimSpace(native)) {
	case "":
		return ""
	// "finished" and "stop_criteria_met" are Amazon Titan's documented
	// completionReason values for a normal completion; without them a Titan
	// response reported its native uppercase string as the finish_reason, which
	// a client checking for "stop" reads as an unfinished answer.
	case "stop", "end_turn", "stop_sequence", "complete", "finish", "finished", "stop_criteria_met":
		return FinishReasonStop
	case "length", "max_tokens", "model_length", "max_completion_tokens":
		return FinishReasonLength
	case "tool_use", "tool_call", "tool_calls", "function_call":
		return FinishReasonToolCalls
	case "content_filtered", "content_filter", "refusal", "safety", "error_toxic",
		// Gemini content-blocking reasons.
		"recitation", "blocklist", "prohibited_content", "spii", "image_safety":
		return FinishReasonContentFilter
	default:
		return native
	}
}
