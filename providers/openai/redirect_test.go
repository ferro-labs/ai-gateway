package openai

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// TestSDKSurfacesRedirectAsError pins REG-003.
//
// The gateway's outbound clients surface a 3xx rather than following it
// (SEC-002), so the openai-go SDK can now receive one. Its success/failure
// boundary is `>= 400`, so a 3xx falls through to the SUCCESS decoder: a 302
// carrying a JSON body used to return err=nil with an empty payload, which the
// gateway then served as HTTP 200 with zero embeddings costed at zero — silent
// wrong data.
//
// Both SDK-backed surfaces must instead report a classified error.
func TestSDKSurfacesRedirectAsError(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		contentType string
		body        string
	}{
		// The silent-success shape: a redirect whose body decodes cleanly.
		{"302 with a JSON body", http.StatusFound, "application/json", `{}`},
		{"307 with an empty JSON object", http.StatusTemporaryRedirect, "application/json", `{}`},
		// The diagnostically-hostile shape: no content type, no body.
		{"302 with no body", http.StatusFound, "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", "http://127.0.0.1:1/elsewhere")
				if tc.contentType != "" {
					w.Header().Set("Content-Type", tc.contentType)
				}
				w.WriteHeader(tc.status)
				if tc.body != "" {
					if _, err := w.Write([]byte(tc.body)); err != nil {
						t.Errorf("write body: %v", err)
					}
				}
			}))
			defer srv.Close()

			p, err := New("sentinel-not-a-real-credential-0005", srv.URL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			t.Run("Embed", func(t *testing.T) {
				resp, err := p.Embed(t.Context(), core.EmbeddingRequest{
					Model: "text-embedding-3-small",
					Input: "hi",
				})
				if err == nil {
					t.Fatalf("Embed returned nil error on HTTP %d; got resp=%+v — a redirect is not a successful embedding", tc.status, resp)
				}
				assertCarriesStatus(t, err, tc.status)
			})

			t.Run("GenerateImage", func(t *testing.T) {
				resp, err := p.GenerateImage(t.Context(), core.ImageRequest{
					Model:  "gpt-image-1-mini",
					Prompt: "a red square",
				})
				if err == nil {
					t.Fatalf("GenerateImage returned nil error on HTTP %d; got resp=%+v", tc.status, resp)
				}
				assertCarriesStatus(t, err, tc.status)
			})
		})
	}
}

// bodilessTransport answers with a redirect carrying no Body at all — the shape
// a hand-written test double produces and a real transport never does.
type bodilessTransport struct{}

func (bodilessTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusFound, Header: http.Header{}}, nil
}

// TestRedirectGuard_NilBody keeps redirectGuard reporting the redirect rather
// than panicking on it. http.RoundTripper is an interface, so the guard cannot
// assume the Body its own code closes was ever set.
func TestRedirectGuard_NilBody(t *testing.T) {
	//nolint:bodyclose // the assertion below is that no response is returned at all; the guard closed what there was.
	resp, err := (redirectGuard{base: bodilessTransport{}}).RoundTrip(
		httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://example.invalid/v1/embeddings", nil))
	if resp != nil {
		t.Errorf("response = %+v, want nil alongside the classified error", resp)
	}
	assertCarriesStatus(t, err, http.StatusFound)
}

// assertCarriesStatus requires the error to carry the upstream status, so the
// gateway can classify it instead of reporting an opaque decode failure.
func assertCarriesStatus(t *testing.T, err error, want int) {
	t.Helper()
	var se *core.HTTPStatusError
	if !errors.As(err, &se) {
		t.Errorf("error does not carry an HTTP status (%T: %v); the gateway cannot classify it", err, err)
		return
	}
	if se.StatusCode != want {
		t.Errorf("status = %d, want %d", se.StatusCode, want)
	}
}
