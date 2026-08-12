package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBearerHeaderSentAndOmitted(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		writeJSON(t, w, http.StatusOK, `{}`)
	}))
	defer srv.Close()

	c, _ := New(srv.URL, "fgw_secret")
	var out map[string]any
	if _, err := c.do(context.Background(), http.MethodGet, "/x", nil, nil, &out, doOpts{}); err != nil {
		t.Fatal(err)
	}
	if got != "Bearer fgw_secret" {
		t.Fatalf("want bearer header, got %q", got)
	}

	c2, _ := New(srv.URL, "")
	if _, err := c2.do(context.Background(), http.MethodGet, "/x", nil, nil, &out, doOpts{}); err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("empty key must send no Authorization header, got %q", got)
	}
}

func TestRedirectRefusedNeverFollowed(t *testing.T) {
	var followed atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		followed.Add(1)
	}))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/steal", http.StatusFound)
	}))
	defer srv.Close()

	c, _ := New(srv.URL, "fgw_secret")
	var out map[string]any
	_, err := c.do(context.Background(), http.MethodGet, "/x", nil, nil, &out, doOpts{})
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusFound {
		t.Fatalf("want api.Error 302, got %v", err)
	}
	if !strings.Contains(apiErr.RedirectTo, target.URL) {
		t.Fatalf("error must surface the redirect target, got %+v", apiErr)
	}
	if followed.Load() != 0 {
		t.Fatal("redirect was followed — bearer token would have replayed")
	}
}

func TestEmpty2xxWithExpectedPayloadIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK) // no body
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "")
	var out map[string]any
	_, err := c.do(context.Background(), http.MethodGet, "/x", nil, nil, &out, doOpts{})
	if err == nil || !strings.Contains(err.Error(), "empty response body") {
		t.Fatalf("want empty-2xx refusal, got %v", err)
	}
}

func Test204WithNilDestIsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "")
	if _, err := c.do(context.Background(), http.MethodDelete, "/x", nil, nil, nil, doOpts{}); err != nil {
		t.Fatalf("204 with nil dest must succeed: %v", err)
	}
}

func TestErrorEnvelopeDecoded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusUnauthorized,
			`{"error":{"message":"bad key","type":"authentication_error","code":"unauthorized"}}`)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "fgw_bad")
	var out map[string]any
	_, err := c.do(context.Background(), http.MethodGet, "/x", nil, nil, &out, doOpts{})
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Message != "bad key" || apiErr.Type != "authentication_error" {
		t.Fatalf("envelope not decoded: %v", err)
	}
}

func TestTolerated503DecodesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusServiceUnavailable,
			`{"status":"not_ready","reason":"no routable targets"}`)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "")
	var out struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	status, err := c.do(context.Background(), http.MethodGet, "/readyz", nil, nil, &out, doOpts{tolerate: []int{503}})
	if err != nil || status != 503 || out.Reason != "no routable targets" {
		t.Fatalf("tolerated 503 must decode: status=%d out=%+v err=%v", status, out, err)
	}
}

func TestNewRejectsUnusableGatewayURLs(t *testing.T) {
	// A query or a fragment is refused rather than dropped: do builds the path
	// and the query of every request, so neither could survive, and deleting
	// the part that cannot work is the same defect one step quieter.
	for _, raw := range []string{
		"", "localhost:8080", "http://", "://nope",
		"http://localhost:8080?token=abc",
		"http://localhost:8080/v1?a=b",
		"http://localhost:8080#frag",
		"http://localhost:8080/?",
	} {
		if c, err := New(raw, ""); err == nil {
			t.Fatalf("New(%q) must fail, got a client for %q", raw, c.BaseURL())
		}
	}
	// A path, a port and userinfo are all legitimate and must survive.
	for _, raw := range []string{
		"http://localhost:8080", "https://gw.example.com/ferro/", "https://u:p@gw.example.com",
	} {
		if _, err := New(raw, ""); err != nil {
			t.Fatalf("New(%q) must succeed: %v", raw, err)
		}
	}
}

// TestNewNormalizesBaseURL pins the exact form BaseURL() returns for every
// accepted input. do and StreamChat both build requests by joining an
// endpoint path onto this string (do via resolveURL's path.Join, StreamChat
// via the same resolveURL call), so a trailing slash surviving here is what
// would put a double slash on every request path, streaming included.
func TestNewNormalizesBaseURL(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"http://localhost:8080", "http://localhost:8080"},
		{"http://localhost:8080/", "http://localhost:8080"},
		// The load-bearing case: a path-prefixed base must lose its trailing
		// slash, or concatenating "/v1/chat/completions" onto it doubles up.
		{"https://gw.example.com/ferro/", "https://gw.example.com/ferro"},
		{"https://gw.example.com/ferro", "https://gw.example.com/ferro"},
		{"https://u:p@gw.example.com", "https://u:p@gw.example.com"},
	}
	for _, tt := range tests {
		c, err := New(tt.raw, "")
		if err != nil {
			t.Fatalf("New(%q): %v", tt.raw, err)
		}
		if got := c.BaseURL(); got != tt.want {
			t.Errorf("New(%q).BaseURL() = %q, want %q", tt.raw, got, tt.want)
		}
		if strings.HasSuffix(c.BaseURL(), "/") {
			t.Errorf("New(%q).BaseURL() = %q must never carry a trailing slash", tt.raw, c.BaseURL())
		}
	}
}

func TestIsNotSupported(t *testing.T) {
	if !IsNotSupported(&Error{Status: 501}) || !IsNotSupported(&Error{Status: 404}) || IsNotSupported(&Error{Status: 500}) {
		t.Fatal("IsNotSupported must be exactly {404, 501}")
	}
}

func TestWithTimeoutUpdatesClientAndHeaderBounds(t *testing.T) {
	c, err := New("http://localhost:8080", "", WithTimeout(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	tr, ok := c.hc.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T", c.hc.Transport)
	}
	if c.hc.Timeout != 3*time.Second || tr.ResponseHeaderTimeout != 3*time.Second {
		t.Fatalf("timeouts diverged: client=%s header=%s", c.hc.Timeout, tr.ResponseHeaderTimeout)
	}
}
