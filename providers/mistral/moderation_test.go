package mistral

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestMistralProvider_Moderate_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.ModerationProvider = p
}

func TestMistralProvider_Moderate_MockHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/moderations" {
			t.Errorf("path = %q, want /v1/moderations", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"mod-1","model":"mistral-moderation-latest","results":[{"flagged":false,"categories":{"selfharm":false},"category_scores":{"selfharm":0.001}}]}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Moderate(context.Background(), core.ModerationRequest{Model: "mistral-moderation-latest", Input: "hello"})
	if err != nil {
		t.Fatalf("Moderate() error: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Flagged {
		t.Fatalf("results = %+v, want one unflagged", resp.Results)
	}
	if resp.Results[0].CategoryScores["selfharm"] != 0.001 {
		t.Errorf("scores wrong: %+v", resp.Results[0])
	}
}
