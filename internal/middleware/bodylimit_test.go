package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestMaxRequestBody_UnderLimit verifies that requests within the limit pass through.
func TestMaxRequestBody_UnderLimit(t *testing.T) {
	const limit = 100

	var (
		gotBody string
		called  bool
	)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	})
	h := MaxRequestBody(limit)(inner)

	body := strings.Repeat("x", 50)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if !called {
		t.Fatal("inner handler was not called")
	}
	if gotBody != body {
		t.Errorf("body = %q, want %q", gotBody, body)
	}
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

// TestMaxRequestBody_OverLimit verifies that the reader errors when reading past the limit.
// The middleware itself wraps the body; it is the handler's responsibility to detect
// *http.MaxBytesError and return 413. This test confirms the limit is actually applied.
func TestMaxRequestBody_OverLimit(t *testing.T) {
	const limit = 10

	var readErr error
	inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	})
	h := MaxRequestBody(limit)(inner)

	body := strings.Repeat("x", 100) // well over limit
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if readErr == nil {
		t.Fatal("expected a read error when body exceeds limit, got nil")
	}
	var maxBytesErr *http.MaxBytesError
	if !asMaxBytesError(readErr, &maxBytesErr) {
		t.Errorf("expected *http.MaxBytesError, got %T: %v", readErr, readErr)
	}
}

// asMaxBytesError is a helper that avoids importing errors in the test file.
func asMaxBytesError(err error, target **http.MaxBytesError) bool {
	for err != nil {
		if mbe, ok := err.(*http.MaxBytesError); ok {
			*target = mbe
			return true
		}
		// unwrap
		type unwrapper interface{ Unwrap() error }
		if u, ok := err.(unwrapper); ok {
			err = u.Unwrap()
		} else {
			break
		}
	}
	return false
}
