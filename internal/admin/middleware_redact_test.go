package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// buildFakeKey joins prefix and body at runtime so no credential-shaped
// literal is committed for a scanner to flag. Mirrors the helper used by
// the redaction policy tests in internal/redact.
func buildFakeKey(prefix, body string) string { return prefix + body }

// A store or validation error can quote text it was handed, so the admin
// error writer filters the message for the same reason the request-path
// writer does. This guards the second envelope writer, which is easy to miss
// when only the request path is considered.
func TestWriteError_RedactsMessage(t *testing.T) {
	key := buildFakeKey("sk-proj-", strings.Repeat("A", 40))

	w := httptest.NewRecorder()
	writeError(w, http.StatusInternalServerError,
		errors.New("config store rejected value "+key).Error(),
		"server_error", "internal_error")

	body := w.Body.String()
	if strings.Contains(body, key) {
		t.Fatalf("admin error response leaked the credential: %s", body)
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
	if got.Error.Type != "server_error" || got.Error.Code != "internal_error" {
		t.Fatalf("redaction altered the envelope: type=%q code=%q", got.Error.Type, got.Error.Code)
	}
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}

func TestWriteError_LeavesCleanMessageIntact(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, http.StatusBadRequest, "name is required", "invalid_request_error", "invalid_request")

	if !strings.Contains(w.Body.String(), "name is required") {
		t.Fatalf("clean message was altered: %s", w.Body.String())
	}
}
