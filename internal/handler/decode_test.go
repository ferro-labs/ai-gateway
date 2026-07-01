package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// decodeTarget is the minimal shape decodeJSONBody decodes into. A small local
// struct keeps the test focused on the shared decoder rather than any real
// request type.
type decodeTarget struct {
	Model string `json:"model"`
}

// TestDecodeJSONBody exercises the three branches of the shared decodeJSONBody
// helper:
//
//	(a) a well-formed JSON body decodes into dst and returns true;
//	(b) a malformed JSON body writes an OpenAI 400 invalid_request error and
//	    returns false;
//	(c) an oversized body wrapped by http.MaxBytesReader triggers the
//	    *http.MaxBytesError path, writes a 413 request_too_large error, and
//	    returns false.
func TestDecodeJSONBody(t *testing.T) {
	tests := []struct {
		name       string
		buildReq   func(w http.ResponseWriter) *http.Request
		wantOK     bool
		wantStatus int
		wantType   string
		wantCode   string
		wantModel  string // only checked when wantOK is true
	}{
		{
			name: "well-formed body decodes successfully",
			buildReq: func(_ http.ResponseWriter) *http.Request {
				return httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"model":"gpt-4o"}`))
			},
			wantOK:    true,
			wantModel: "gpt-4o",
		},
		{
			name: "malformed body returns 400",
			buildReq: func(_ http.ResponseWriter) *http.Request {
				// Truncated JSON: the decoder fails with a non-MaxBytes error.
				return httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"model":`))
			},
			wantOK:     false,
			wantStatus: http.StatusBadRequest,
			wantType:   "invalid_request_error",
			wantCode:   "invalid_request",
		},
		{
			name: "oversized body returns 413",
			buildReq: func(w http.ResponseWriter) *http.Request {
				// Valid-JSON-prefixed body far larger than the tiny limit so the
				// decoder reads partial content, then hits the MaxBytesReader cap.
				body := `{"model":"` + strings.Repeat("x", 500) + `"}`
				r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
				r.Body = http.MaxBytesReader(w, r.Body, 5)
				return r
			},
			wantOK:     false,
			wantStatus: http.StatusRequestEntityTooLarge,
			wantType:   "invalid_request_error",
			wantCode:   "request_too_large",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := tt.buildReq(w)

			var dst decodeTarget
			ok := decodeJSONBody(w, r, &dst)

			if ok != tt.wantOK {
				t.Fatalf("decodeJSONBody returned %v, want %v (body=%s)", ok, tt.wantOK, w.Body.String())
			}

			if tt.wantOK {
				if dst.Model != tt.wantModel {
					t.Errorf("decoded model = %q, want %q", dst.Model, tt.wantModel)
				}
				// The success path must not write an error response.
				if w.Body.Len() != 0 {
					t.Errorf("expected empty response body on success, got %q", w.Body.String())
				}
				return
			}

			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", w.Code, tt.wantStatus, w.Body.String())
			}

			var resp struct {
				Error struct {
					Message string `json:"message"`
					Type    string `json:"type"`
					Code    string `json:"code"`
				} `json:"error"`
			}
			if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if resp.Error.Type != tt.wantType {
				t.Errorf("error type = %q, want %q", resp.Error.Type, tt.wantType)
			}
			if resp.Error.Code != tt.wantCode {
				t.Errorf("error code = %q, want %q", resp.Error.Code, tt.wantCode)
			}
			if resp.Error.Message == "" {
				t.Error("expected non-empty error message")
			}
		})
	}
}
