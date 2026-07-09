package httpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/providers"
)

// panicProvider panics inside the provider call, i.e. below every middleware.
type panicProvider struct{}

func (panicProvider) Name() string              { return "panic-provider" }
func (panicProvider) SupportedModels() []string { return []string{"panic-model"} }
func (panicProvider) SupportsModel(m string) bool {
	return m == "panic-model"
}
func (panicProvider) Models() []providers.ModelInfo {
	return []providers.ModelInfo{{ID: "panic-model", Object: "model", OwnedBy: "panic-provider"}}
}
func (panicProvider) Complete(context.Context, providers.Request) (*providers.Response, error) {
	panic("provider exploded")
}

// A panic anywhere under the middleware stack must surface as the gateway's JSON
// error envelope, with the trace ID that logging.Middleware assigned still on the
// response. This pins RecoverJSON's position at the outside of the chain.
func TestRouter_PanicReturnsJSONEnvelopeWithTraceID(t *testing.T) {
	gw, err := aigateway.New(aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: "panic-provider"}},
	})
	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}
	gw.RegisterProvider(panicProvider{})

	router := buildTestRouter(t, gw)
	body := `{"model":"panic-model","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(body))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	if w.Header().Get("X-Request-ID") == "" {
		t.Fatal("X-Request-ID missing: logging.Middleware ran but its header did not survive recovery")
	}
	if w.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("Content-Security-Policy missing from the recovered error response")
	}

	var payload struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Error.Type != "server_error" || payload.Error.Code != "internal_error" {
		t.Fatalf("error = %#v, want server_error/internal_error", payload.Error)
	}
	if strings.Contains(payload.Error.Message, "exploded") {
		t.Fatalf("panic detail leaked to the client: %q", payload.Error.Message)
	}
}
