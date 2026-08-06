package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A 2xx carrying no body is success only when the caller expected no payload.
//
// The status check bounds what counts as failure; it does not check that the
// answer arrived. A caller that passed a destination asked for one, so an empty
// 2xx means the operation did not produce what it claims to have produced —
// `ferrogw admin keys create` printed `null` and exited 0, which a script reads
// as a key it never received.
//
// This is REG-002's harm on the adjacent status band: there the failure was a
// bodyless redirect, here it is a bodyless success.
func TestAdminClientRefusesAnEmptySuccessWhenAPayloadWasExpected(t *testing.T) {
	newClient := func(t *testing.T, status int, body string) *AdminClient {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			if body != "" {
				if _, err := w.Write([]byte(body)); err != nil {
					t.Errorf("write body: %v", err)
				}
			}
		}))
		t.Cleanup(srv.Close)
		return &AdminClient{
			BaseURL:    srv.URL,
			APIKey:     "sentinel-not-a-real-credential-0007",
			HTTPClient: &http.Client{Timeout: 5 * time.Second},
		}
	}

	type payload struct {
		Key string `json:"key"`
	}

	t.Run("empty 200 with a destination is a failure", func(t *testing.T) {
		var got payload
		err := newClient(t, http.StatusOK, "").Post(context.Background(), "/admin/keys", nil, &got)
		if err == nil {
			t.Fatal("Post returned nil; an empty 200 must not read as a created key")
		}
		if !strings.Contains(err.Error(), "empty response") {
			t.Errorf("error = %q, want it to name the empty response", err)
		}
	})

	t.Run("204 with a destination is a failure", func(t *testing.T) {
		var got payload
		err := newClient(t, http.StatusNoContent, "").Post(context.Background(), "/admin/keys", nil, &got)
		if err == nil {
			t.Fatal("Post returned nil; a 204 carries no payload to decode")
		}
	})

	// The counterpart, and the reason this is not simply "a 2xx must have a
	// body": revoke really does answer 204, and asks for nothing back.
	t.Run("204 with no destination is success", func(t *testing.T) {
		if err := newClient(t, http.StatusNoContent, "").Post(context.Background(), "/admin/keys/k/revoke", nil, nil); err != nil {
			t.Errorf("Post = %v, want nil: a 204 is this endpoint's documented success", err)
		}
	})

	t.Run("a populated 201 still decodes", func(t *testing.T) {
		var got payload
		if err := newClient(t, http.StatusCreated, `{"key":"fgw_x"}`).Post(context.Background(), "/admin/keys", nil, &got); err != nil {
			t.Fatalf("Post = %v, want nil", err)
		}
		if got.Key != "fgw_x" {
			t.Errorf("key = %q, want fgw_x", got.Key)
		}
	})
}
