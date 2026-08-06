package core

// SpeechRequest mirrors the OpenAI /v1/audio/speech (text-to-speech) JSON
// request. It is decoded from the request body by the handler, so its fields
// carry json tags.
type SpeechRequest struct {
	Model string `json:"model"`
	// Input is the text to synthesize.
	Input string `json:"input"`
	// Voice names the voice. Built-in names (alloy, nova, …) for OpenAI-style
	// providers; a provider-specific id for others.
	Voice string `json:"voice"`
	// ResponseFormat is mp3 (default) | opus | aac | flac | wav | pcm.
	ResponseFormat string `json:"response_format,omitempty"`
	// Speed is 0.25–4.0 (default 1.0). Not every provider honours it.
	Speed *float64 `json:"speed,omitempty"`
}

// SpeechResponse is the gateway-standard text-to-speech result: the decoded
// audio bytes plus the media type to serve them as. A provider returns already
// decoded bytes — a raw-audio upstream passes its body through, a base64-in-JSON
// upstream decodes first — so the handler is identical for both.
type SpeechResponse struct {
	Audio       []byte
	ContentType string
}
