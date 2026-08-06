package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestOpenAIProvider_Moderate_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.ModerationProvider = p
}

func TestOpenAIProvider_Moderate_MockHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/moderations" {
			t.Errorf("path = %q, want /v1/moderations", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"modr-1","model":"omni-moderation-latest","results":[{"flagged":true,"categories":{"violence":true,"hate":false},"category_scores":{"violence":0.9,"hate":0.01}}]}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Moderate(context.Background(), core.ModerationRequest{Model: "omni-moderation-latest", Input: "some text"})
	if err != nil {
		t.Fatalf("Moderate() error: %v", err)
	}
	if len(resp.Results) != 1 || !resp.Results[0].Flagged {
		t.Fatalf("results = %+v, want one flagged", resp.Results)
	}
	if !resp.Results[0].Categories["violence"] || resp.Results[0].CategoryScores["violence"] != 0.9 {
		t.Errorf("categories/scores wrong: %+v", resp.Results[0])
	}
}

func TestOpenAIProvider_Moderate_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.Moderate(context.Background(), core.ModerationRequest{Model: "omni-moderation-latest", Input: "x"})
	if err == nil {
		t.Fatal("Moderate() error = nil, want 401")
	}
	if core.ParseStatusCode(err) != http.StatusUnauthorized {
		t.Errorf("ParseStatusCode = %d, want 401", core.ParseStatusCode(err))
	}
}
