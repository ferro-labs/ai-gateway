package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRecoverJSONReturnsErrorEnvelope(t *testing.T) {
	handler := RecoverJSON(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/panic", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Error.Code != "internal_error" || payload.Error.Type != "server_error" {
		t.Fatalf("error = %#v, want internal_error/server_error", payload.Error)
	}
	if strings.Contains(payload.Error.Message, "boom") {
		t.Fatalf("panic detail leaked in response message: %q", payload.Error.Message)
	}
}

func TestRecoverJSONRepanicsAbortHandler(t *testing.T) {
	handler := RecoverJSON(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/abort", nil)
	w := httptest.NewRecorder()

	defer func() {
		recovered := recover()
		if recovered != http.ErrAbortHandler {
			t.Fatalf("recovered = %v, want http.ErrAbortHandler", recovered)
		}
		if w.Body.Len() != 0 {
			t.Fatalf("response body should stay empty, got %q", w.Body.String())
		}
	}()
	handler.ServeHTTP(w, req)
}
