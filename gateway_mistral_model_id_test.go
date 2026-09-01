package aigateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/mistral"
)

// TestRoute_MistralServedModelIDReachesUpstreamVerbatim pins the model id the
// gateway puts on the wire for a Mistral model whose name carries a
// vendor-family prefix ("open-mistral-nemo").
//
// The adapter's own guard lives in providers/mistral; this one covers the whole
// routing path — alias resolution, the model index, and the target walk — because
// the id a caller sends and the id a provider is handed pass through gateway code
// that could rewrite either. The assertion is on the serialized body the stub
// upstream received, so it holds regardless of what any intermediate struct says.
func TestRoute_MistralServedModelIDReachesUpstreamVerbatim(t *testing.T) {
	const servedModel = "open-mistral-nemo"

	var raw []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		raw = body
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","model":"` + servedModel + `","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	// Pin the catalog to the embedded copy so the test neither reaches the
	// network nor changes behaviour when the remote catalog does.
	t.Setenv(models.CatalogURLEnv, "file:///ferro-tests-use-embedded-catalog")

	gw, err := New(config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: mistral.Name}},
	})
	if err != nil {
		t.Fatalf("create gateway: %v", err)
	}
	defer func() { _ = gw.Close() }()

	p, err := mistral.New("test-key", upstream.URL)
	if err != nil {
		t.Fatalf("create mistral provider: %v", err)
	}
	gw.RegisterProvider(p)

	if _, err := gw.Route(context.Background(), providers.Request{
		Model:    servedModel,
		Messages: []providers.Message{{Role: "user", Content: "Hi"}},
	}); err != nil {
		t.Fatalf("Route() error: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode outbound body: %v (body=%s)", err, raw)
	}
	if got := body["model"]; got != servedModel {
		t.Errorf("upstream received model %#v, want %q (the gateway remapped the served model id)", got, servedModel)
	}
}
