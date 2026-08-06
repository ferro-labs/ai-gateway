package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestDiscoverModels_RequestsFullPage verifies discovery asks for the whole
// model list rather than accepting Anthropic's paginated default.
//
// /v1/models is paginated and defaults to 20 items per page. The shared
// discovery helper reads one page and has no has_more/last_id loop — deliberately,
// since 19 other providers call it — so the default silently truncated the model
// list here. limit=1000 is Anthropic's documented maximum and is far above the
// number of models it has ever offered at once, which makes one page the whole
// list without putting a pagination loop in the shared path.
func TestDiscoverModels_RequestsFullPage(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		if got := r.Header.Get("x-api-key"); got != "test-key" {
			t.Errorf("x-api-key = %q, want %q", got, "test-key")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-sonnet-4-5","created_at":"2025-09-29T00:00:00Z"}],"has_more":false}`))
	}))
	defer srv.Close()

	p, err := New("test-key", srv.URL)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	models, err := p.DiscoverModels(context.Background())
	if err != nil {
		t.Fatalf("DiscoverModels(): %v", err)
	}
	if got := len(models); got != 1 {
		t.Fatalf("DiscoverModels() returned %d models, want 1", got)
	}
	if models[0].ID != "claude-sonnet-4-5" {
		t.Errorf("model id = %q, want %q", models[0].ID, "claude-sonnet-4-5")
	}
	if gotQuery != "limit=1000" {
		t.Errorf("discovery query = %q, want %q (the default page holds only 20 models)", gotQuery, "limit=1000")
	}
}
