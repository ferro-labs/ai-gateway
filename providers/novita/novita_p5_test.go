package novita

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// TestNew_RejectsInvalidBaseURL verifies the constructor fails fast when the base
// URL is not a valid absolute http(s) URL with a host.
func TestNew_RejectsInvalidBaseURL(t *testing.T) {
	if _, err := New("test-key", "://nope"); err == nil {
		t.Fatal("New() accepted an invalid base URL, want error")
	}
}

// TestNovitaProvider_Complete_UpstreamError verifies a non-2xx chat response
// surfaces an error carrying both the HTTP status and the upstream message.
func TestNovitaProvider_Complete_UpstreamError(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		message string
	}{
		{"rate-limited", http.StatusTooManyRequests, "slow down"},
		{"server-error", http.StatusInternalServerError, "internal boom"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != testChatPath {
					t.Errorf("path = %q, want %s", r.URL.Path, testChatPath)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"error":{"message":"` + tc.message + `"}}`))
			}))
			defer srv.Close()

			p, _ := New("test-key", srv.URL)
			_, err := p.Complete(context.Background(), core.Request{
				Model:    testChatModel,
				Messages: []core.Message{{Role: "user", Content: "Hi"}},
			})
			if err == nil {
				t.Fatal("Complete() error = nil, want upstream error")
			}
			if got := core.ParseStatusCode(err); got != tc.status {
				t.Errorf("ParseStatusCode(err) = %d, want %d", got, tc.status)
			}
			if !strings.Contains(err.Error(), "novita API error") {
				t.Errorf("error = %v, want it to contain %q", err, "novita API error")
			}
			if !strings.Contains(err.Error(), tc.message) {
				t.Errorf("error = %v, want it to contain %q", err, tc.message)
			}
		})
	}
}

// TestNovitaProvider_CompleteStream_UpstreamError verifies a non-2xx streaming
// response is drained and surfaced as an error before any chunk is produced.
func TestNovitaProvider_CompleteStream_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != testChatPath {
			t.Errorf("path = %q, want %s", r.URL.Path, testChatPath)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream unavailable"}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    testChatModel,
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("CompleteStream() error = nil, want upstream error")
	}
	if ch != nil {
		t.Error("CompleteStream() channel = non-nil, want nil on error")
	}
	if got := core.ParseStatusCode(err); got != http.StatusServiceUnavailable {
		t.Errorf("ParseStatusCode(err) = %d, want %d", got, http.StatusServiceUnavailable)
	}
	if !strings.Contains(err.Error(), "novita API error") {
		t.Errorf("error = %v, want it to contain %q", err, "novita API error")
	}
	if !strings.Contains(err.Error(), "upstream unavailable") {
		t.Errorf("error = %v, want it to contain %q", err, "upstream unavailable")
	}
}

// TestNovitaProvider_DiscoverModels verifies live discovery issues a GET to
// /models with bearer auth and parses the returned model metadata.
func TestNovitaProvider_DiscoverModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if r.URL.Path != "/models" {
			t.Errorf("path = %q, want /models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != testBearerAPIKey {
			t.Errorf("Authorization = %q, want %s", got, testBearerAPIKey)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"` + testChatModel + `","object":"model","created":1700000000,"owned_by":"novita"}]}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	models, err := p.DiscoverModels(context.Background())
	if err != nil {
		t.Fatalf("DiscoverModels() error: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	if models[0].ID != testChatModel {
		t.Errorf("model[0].ID = %q, want %s", models[0].ID, testChatModel)
	}
	if models[0].OwnedBy != "novita" {
		t.Errorf("model[0].OwnedBy = %q, want novita", models[0].OwnedBy)
	}
}
