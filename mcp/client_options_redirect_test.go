package mcp

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestClientOptionsTransportRetainsRedirectProtection(t *testing.T) {
	for _, host := range []string{"same host", "different host"} {
		for _, notification := range []bool{false, true} {
			name := host + "/call"
			if notification {
				name = host + "/notification"
			}
			t.Run(name, func(t *testing.T) {
				var originRequests, targetRequests atomic.Int32
				target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					targetRequests.Add(1)
					w.WriteHeader(http.StatusOK)
				}))
				defer target.Close()
				location := target.URL
				if host == "different host" {
					location = strings.Replace(location, "127.0.0.1", "localhost", 1)
				}
				origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					originRequests.Add(1)
					if r.Header.Get("X-Api-Key") == "" || r.Header.Get("Authorization") == "" {
						t.Error("origin did not receive test credentials")
					}
					http.Redirect(w, r, location, http.StatusTemporaryRedirect)
				}))
				defer origin.Close()
				original := &http.Client{Transport: origin.Client().Transport}
				c, err := NewClientWithOptions(origin.URL, map[string]string{"X-Api-Key": "synthetic-example", "Authorization": "Bearer synthetic-example"}, 0, ClientOptions{HTTPClient: original})
				if err != nil {
					t.Fatal(err)
				}
				if notification {
					_ = c.notify(t.Context(), "notifications/initialized", nil)
				} else if _, err := c.ListTools(t.Context()); err == nil {
					t.Error("redirected call succeeded")
				}
				if got := targetRequests.Load(); got != 0 {
					t.Errorf("redirect target received %d requests; want none", got)
				}
				if got := originRequests.Load(); got != 1 {
					t.Errorf("origin received %d requests; want 1", got)
				}
				if original.CheckRedirect != nil {
					t.Error("constructor mutated caller redirect policy")
				}
			})
		}
	}
}

func TestClientOptionsPreservesExplicitRedirectPolicy(t *testing.T) {
	want := errors.New("application redirect policy")
	var calls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	original := &http.Client{Transport: origin.Client().Transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		calls.Add(1)
		return want
	}}
	c, err := NewClientWithOptions(origin.URL, nil, 0, ClientOptions{HTTPClient: original})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListTools(t.Context()); !errors.Is(err, want) {
		t.Fatalf("explicit redirect policy lost: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("policy calls = %d; want 1", calls.Load())
	}
}
