package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestUpdateConfigReportsEveryUnknownField holds PUT /admin/config to the same
// one-edit-per-fix contract a config file gets.
//
// This body is hand-written — no other source supplies it — so several typos in
// one submission is the normal case, not the exotic one. Reporting the first
// and stopping makes correcting it one round trip per typo, and the operator
// cannot tell after the first fix whether the next response will accept the
// config or name another key. All of them, or the endpoint is a guessing game.
func TestUpdateConfigReportsEveryUnknownField(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	body := `{"strategy":{"modee":"fallback"},"aliasez":{},` +
		`"targets":[{"virtual_key":"openai","model":["gpt-4o-mini"],"weightt":1}]}`
	req := authedRequest(http.MethodPut, "/admin/config", body, adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown fields, got %d: %s", w.Code, w.Body.String())
	}

	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	for _, key := range []string{"modee", "aliasez", "model", "weightt"} {
		if !strings.Contains(envelope.Error.Message, key) {
			t.Errorf("response should name every unknown key; missing %q in %s", key, envelope.Error.Message)
		}
	}
}

// TestUpdateConfigKeepsStdlibErrorForTypeMismatch keeps the enumeration scoped
// to unknown keys: a wrong value type is still reported as a wrong value type,
// not relabelled as a wrong key.
func TestUpdateConfigKeepsStdlibErrorForTypeMismatch(t *testing.T) {
	h, r := setupTestRouter()
	adminKey := createAdminKey(t, h)

	body := `{"strategy":{"mode":"fallback"},"targets":[{"virtual_key":123}]}`
	req := authedRequest(http.MethodPut, "/admin/config", body, adminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a type mismatch, got %d: %s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); !strings.Contains(got, "cannot unmarshal") ||
		strings.Contains(got, "unknown field") {
		t.Errorf("type mismatch should keep the stdlib wording, got %s", got)
	}
}
