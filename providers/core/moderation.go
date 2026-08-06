package core

// ModerationRequest mirrors the OpenAI /v1/moderations request — the shape
// LiteLLM and Portkey expose and the two providers that offer moderation
// (OpenAI, Mistral) both speak.
type ModerationRequest struct {
	Input any    `json:"input"` // string or []string
	Model string `json:"model,omitempty"`
}

// ModerationResponse mirrors the OpenAI /v1/moderations response.
type ModerationResponse struct {
	ID      string             `json:"id"`
	Model   string             `json:"model"`
	Results []ModerationResult `json:"results"`
}

// ModerationResult is one input's classification. Categories and scores are
// maps rather than a fixed struct so each provider's own category vocabulary
// passes through unchanged — OpenAI and Mistral publish different category sets,
// and the map marshals to the identical JSON a fixed struct would.
type ModerationResult struct {
	Flagged        bool               `json:"flagged"`
	Categories     map[string]bool    `json:"categories"`
	CategoryScores map[string]float64 `json:"category_scores"`
	// CategoryAppliedInputTypes is OpenAI's omni-moderation multimodal field;
	// carried through when present.
	CategoryAppliedInputTypes map[string][]string `json:"category_applied_input_types,omitempty"`
}
