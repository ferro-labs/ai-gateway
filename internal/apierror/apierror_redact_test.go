package apierror

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeUpstreamKey has the legacy OpenAI sk- key shape. It is not a real credential.
const fakeUpstreamKey = "sk-abc123DEF456ghi789JKL012mno345"

// An upstream provider controls its own error body, and that body can quote the
// credential the gateway presented. WriteOpenAI must not hand it to the client.
func TestWriteOpenAI_RedactsMessage(t *testing.T) {
	w := httptest.NewRecorder()
	WriteOpenAI(w,
		http.StatusUnauthorized,
		"openai API error (401): Incorrect API key provided: "+fakeUpstreamKey,
		errTypeServer,
		"provider_error",
	)

	body := w.Body.String()
	if strings.Contains(body, fakeUpstreamKey) {
		t.Fatalf("response leaked the upstream key: %s", body)
	}
	if !strings.Contains(body, "[REDACTED_OPENAI_KEY]") {
		t.Errorf("expected a redaction token in the body, got: %s", body)
	}
}

// Redaction must change only the message value. The envelope clients switch on
// — status, type, code — is written exactly as given.
func TestWriteOpenAI_RedactionPreservesEnvelope(t *testing.T) {
	w := httptest.NewRecorder()
	WriteOpenAI(w,
		http.StatusTooManyRequests,
		"rate limited for someone@example.com",
		errTypeRateLimit,
		"rate_limit_exceeded",
	)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want %d", w.Code, http.StatusTooManyRequests)
	}

	var got struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Error.Type != errTypeRateLimit {
		t.Errorf("type = %q, want %q", got.Error.Type, errTypeRateLimit)
	}
	if got.Error.Code != "rate_limit_exceeded" {
		t.Errorf("code = %q, want %q", got.Error.Code, "rate_limit_exceeded")
	}
	if strings.Contains(got.Error.Message, "someone@example.com") {
		t.Errorf("message leaked the address: %q", got.Error.Message)
	}
}

// A message with nothing sensitive in it must survive byte-for-byte, so
// redaction does not quietly reword ordinary gateway errors.
func TestWriteOpenAI_LeavesCleanMessageIntact(t *testing.T) {
	const msg = "no provider supports model: gpt-4o"
	w := httptest.NewRecorder()
	WriteOpenAI(w, http.StatusBadRequest, msg, errTypeInvalidRequest, "model_not_found")

	var got struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Error.Message != msg {
		t.Errorf("message = %q, want %q", got.Error.Message, msg)
	}
}
