package core

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// TestStatusError_MessageIsCallerFacingAndErrorIsOperatorFacing pins the split
// the whole contract rests on. internal/apierror hands Message straight to the
// caller on a 400/422; it therefore must not name the backend. Error() is the
// log line and must.
func TestStatusError_MessageIsCallerFacingAndErrorIsOperatorFacing(t *testing.T) {
	err := StatusError("anthropic", http.StatusBadRequest, "max_tokens is too large")

	if strings.Contains(err.Message, "anthropic") {
		t.Errorf("Message = %q — the caller-facing text discloses which backend served the request", err.Message)
	}
	if strings.Contains(err.Message, "400") {
		t.Errorf("Message = %q — the status is a field, it does not belong in the text too", err.Message)
	}
	if err.Message != "max_tokens is too large" {
		t.Errorf("Message = %q, want the upstream's own sentence verbatim", err.Message)
	}
	if got, want := err.Error(), "anthropic API error (400): max_tokens is too large"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// TestAPIError_UnrecognisedBodyNeverSurfaces is the F55 regression guard.
//
// An upstream error body is not necessarily written by the upstream: a WAF or a
// load balancer in front of it answers with its own page. One such page carried
// an internal hostname, an internal IP, an account id, a live credential and a
// filesystem path, and every one of them reached the client because the
// constructor used the body as the message whenever it could not parse it.
func TestAPIError_UnrecognisedBodyNeverSurfaces(t *testing.T) {
	waf := `<html><h1>403 Forbidden</h1><p>upstream=prod-db-07.internal (10.0.4.19) ` +
		`account=99321 key=sk-live-ABCDEF123456 at /var/www/app/config.php</p></html>`

	for _, body := range []string{waf, `weird body`, ``, `[1,2,3]`, `{"unknown":"shape"}`} {
		err := APIError("openai", http.StatusBadRequest, []byte(body))
		var statusErr *HTTPStatusError
		if !errors.As(err, &statusErr) {
			t.Fatalf("APIError did not return a *HTTPStatusError for body %q", body)
		}
		if statusErr.StatusCode != http.StatusBadRequest {
			t.Errorf("StatusCode = %d, want 400", statusErr.StatusCode)
		}
		if statusErr.Message != http.StatusText(http.StatusBadRequest) {
			t.Errorf("body %q: Message = %q, want the generic status phrase", body, statusErr.Message)
		}
		for _, secret := range []string{"prod-db-07", "10.0.4.19", "99321", "sk-live-ABCDEF123456", "/var/www"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("body %q: error text leaks %q", body, secret)
			}
		}
	}
}

// TestStatusError_BoundsTheMessage guards the other half: even a body in a
// recognised shape is written by someone else and is not length-checked by
// anyone.
func TestStatusError_BoundsTheMessage(t *testing.T) {
	long := strings.Repeat("a", MaxUpstreamMessageRunes*3)
	err := APIError("openai", 400, []byte(fmt.Sprintf(`{"error":{"message":%q}}`, long)))

	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) {
		t.Fatal("APIError did not return a *HTTPStatusError")
	}
	if n := utf8.RuneCountInString(statusErr.Message); n > MaxUpstreamMessageRunes+1 {
		t.Errorf("Message is %d runes, want at most %d plus the ellipsis", n, MaxUpstreamMessageRunes+1)
	}
	if !strings.HasSuffix(statusErr.Message, "…") {
		t.Error("a truncated message must say so")
	}
}

// TestStatusError_TruncationKeepsValidUTF8 — a byte-wise cut through a
// multi-byte rune produces invalid UTF-8, which JSON encoding then renders as
// U+FFFD in the client's error body.
func TestStatusError_TruncationKeepsValidUTF8(t *testing.T) {
	// Three bytes per rune, so a byte-wise cut at any 2048 would land mid-rune.
	err := StatusError("p", 400, strings.Repeat("世", MaxUpstreamMessageRunes+10))
	if !utf8.ValidString(err.Message) {
		t.Error("truncation produced invalid UTF-8")
	}
	if got := utf8.RuneCountInString(err.Message); got != MaxUpstreamMessageRunes+1 {
		t.Errorf("Message = %d runes, want %d + the ellipsis", got, MaxUpstreamMessageRunes)
	}
}

// TestStatusError_EmptyMessageFallsBackToStatusPhrase — an empty message would
// render as "openai API error (429): " and tell a caller on a 400 nothing at all.
func TestStatusError_EmptyMessageFallsBackToStatusPhrase(t *testing.T) {
	if got := StatusError("openai", 429, "   ").Message; got != "Too Many Requests" {
		t.Errorf("Message = %q, want the status phrase", got)
	}
	// A status with no registered phrase still says something.
	if got := StatusError("openai", 599, "").Message; got != "unexpected response" {
		t.Errorf("Message = %q, want a non-empty fallback", got)
	}
}

// TestAPIErrorFromResponse_CarriesRetryAfterAndStatus covers the constructor
// every raw-HTTP provider actually calls.
func TestAPIErrorFromResponse_CarriesRetryAfterAndStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"slow down"}}`))
	}))
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL) //nolint:noctx // local stub, no cancellation to model
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	provErr := APIErrorFromResponse("groq", resp, []byte(`{"error":{"message":"slow down"}}`))
	var statusErr *HTTPStatusError
	if !errors.As(provErr, &statusErr) {
		t.Fatal("APIErrorFromResponse did not return a *HTTPStatusError")
	}
	if statusErr.StatusCode != http.StatusTooManyRequests {
		t.Errorf("StatusCode = %d, want 429", statusErr.StatusCode)
	}
	if statusErr.Provider != "groq" {
		t.Errorf("Provider = %q, want %q", statusErr.Provider, "groq")
	}
	if statusErr.RetryAfter != 7*time.Second {
		t.Errorf("RetryAfter = %v, want 7s", statusErr.RetryAfter)
	}
	if got := RetryAfterFrom(fmt.Errorf("attempt 2: %w", provErr)); got != 7*time.Second {
		t.Errorf("RetryAfterFrom through a wrapper = %v, want 7s", got)
	}
}

// TestHTTPStatusError_SurvivesWrapping is the property every consumer depends
// on: the fallback strategy, the circuit breaker and the HTTP layer all see the
// error only after the routing narrative has been wrapped around it.
func TestHTTPStatusError_SurvivesWrapping(t *testing.T) {
	inner := APIError("openai", 429, []byte(`{"error":{"message":"slow down"}}`))
	wrapped := fmt.Errorf("all providers failed: %w",
		fmt.Errorf("provider openai attempt 3: %w", inner))

	if got := ParseStatusCode(wrapped); got != 429 {
		t.Errorf("ParseStatusCode through two wrappers = %d, want 429", got)
	}
	var statusErr *HTTPStatusError
	if !errors.As(wrapped, &statusErr) || statusErr.Message != "slow down" {
		t.Errorf("errors.As recovered %+v, want the innermost upstream message", statusErr)
	}
}
