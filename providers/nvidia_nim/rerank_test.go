package nvidianim

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestNvidiaNIMProvider_Rerank_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.RerankProvider = p
}

func TestNvidiaNIMProvider_Rerank_MockHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/ranking" {
			t.Errorf("path = %q, want /v1/ranking", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// index 1 (logit 0 -> sigmoid 0.5) outranks index 0 (logit -2 -> ~0.119).
		_, _ = w.Write([]byte(`{"rankings":[{"index":1,"logit":0.0},{"index":0,"logit":-2.0}],"usage":{"prompt_tokens":10,"total_tokens":10}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Rerank(context.Background(), core.RerankRequest{
		Model:     "nvidia/nv-rerankqa-mistral-4b-v3",
		Query:     "q",
		Documents: []string{"doc a", "doc b"},
	})
	if err != nil {
		t.Fatalf("Rerank() error: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(resp.Results))
	}
	if resp.Results[0].Index != 1 {
		t.Errorf("top index = %d, want 1", resp.Results[0].Index)
	}
	if got := resp.Results[0].RelevanceScore; got < 0.49 || got > 0.51 {
		t.Errorf("top score = %v, want ~0.5 (sigmoid of logit 0)", got)
	}
	if resp.Meta == nil || resp.Meta.Tokens == nil || resp.Meta.Tokens.InputTokens != 10 {
		t.Errorf("meta tokens mismatch: %+v", resp.Meta)
	}
}
