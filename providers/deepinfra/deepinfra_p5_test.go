package deepinfra

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

const (
	testChatModel = "deepseek-ai/DeepSeek-R1"
	testChatPath  = "/chat/completions"
)

// TestNew_RejectsInvalidBaseURL verifies the constructor fails fast when the base
// URL is not a valid absolute http(s) URL with a host.
func TestNew_RejectsInvalidBaseURL(t *testing.T) {
	if _, err := New("test-key", "://nope"); err == nil {
		t.Fatal("New() accepted an invalid base URL, want error")
	}
}

// TestDeepInfraProvider_Complete_ForwardsRequest verifies the outbound chat
// request shape: a POST to /chat/completions carrying bearer auth and the
// forwarded model, messages, and temperature.
func TestDeepInfraProvider_Complete_ForwardsRequest(t *testing.T) {
	respBody := `{"id":"cmpl-1","model":"` + testChatModel + `","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != testChatPath {
			t.Errorf("path = %q, want %s", r.URL.Path, testChatPath)
		}
		if got := r.Header.Get("Authorization"); got != testBearerAPIKey {
			t.Errorf("Authorization = %q, want %s", got, testBearerAPIKey)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if got := body["model"]; got != testChatModel {
			t.Errorf("model = %v, want %s", got, testChatModel)
		}
		msgs, ok := body["messages"].([]any)
		if !ok || len(msgs) == 0 {
			t.Errorf("messages = %#v, want non-empty array", body["messages"])
		}
		if got, ok := body["temperature"].(float64); !ok || got != 0.7 {
			t.Errorf("temperature = %#v, want 0.7", body["temperature"])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	temperature := 0.7
	p, _ := New("test-key", srv.URL)
	resp, err := p.Complete(context.Background(), core.Request{
		Model:       testChatModel,
		Messages:    []core.Message{{Role: "user", Content: "Hi"}},
		Temperature: &temperature,
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.ID != "cmpl-1" {
		t.Errorf("Response.ID = %q, want cmpl-1", resp.ID)
	}
}

// TestDeepInfraProvider_Complete_UpstreamError verifies a non-2xx chat response
// surfaces an error carrying both the HTTP status and the upstream message.
func TestDeepInfraProvider_Complete_UpstreamError(t *testing.T) {
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
			if !strings.Contains(err.Error(), "deepinfra API error") {
				t.Errorf("error = %v, want it to contain %q", err, "deepinfra API error")
			}
			if !strings.Contains(err.Error(), tc.message) {
				t.Errorf("error = %v, want it to contain %q", err, tc.message)
			}
		})
	}
}

// TestDeepInfraProvider_DiscoverModels verifies live discovery issues a GET to
// /models with bearer auth and parses the returned model metadata.
func TestDeepInfraProvider_DiscoverModels(t *testing.T) {
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
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"` + testChatModel + `","object":"model","created":1700000000,"owned_by":"deepinfra"}]}`))
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
	if models[0].OwnedBy != "deepinfra" {
		t.Errorf("model[0].OwnedBy = %q, want deepinfra", models[0].OwnedBy)
	}
}
