package core

// TranscriptionRequest mirrors the OpenAI /v1/audio/transcriptions (and
// /v1/audio/translations) multipart request. It is built from the multipart form
// by the handler, not JSON-decoded, so its fields carry no json tags. File holds
// the raw audio bytes.
type TranscriptionRequest struct {
	Model    string
	File     []byte
	Filename string
	// Language is an ISO-639-1 hint; ignored on the translations path (which
	// always targets English).
	Language string
	Prompt   string
	// ResponseFormat is json (default) | text | srt | verbose_json | vtt.
	ResponseFormat string
	Temperature    *float64
	// Translate selects /v1/audio/translations (translate to English) instead of
	// /v1/audio/transcriptions.
	Translate bool
}

// TranscriptionResponse is the gateway-standard transcription result. Text is
// the transcript regardless of the upstream response_format.
type TranscriptionResponse struct {
	Text string `json:"text"`
}
