package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
)

// TestImages_NoCapableProvider_Returns404 verifies that a request whose model
// has no registered ImageProvider returns HTTP 404 with an OpenAI
// invalid_request_error/model_not_found body rather than 500/routing_error.
// Regression test for the capability-miss-as-server-error bug.
func TestImages_NoCapableProvider_Returns404(t *testing.T) {
	gw, err := aigateway.New(aigateway.Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	body := `{"model":"no-such-image-model","prompt":"a cat"}`
	r := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(body))
	w := httptest.NewRecorder()

	Images(gw)(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d (body=%s)", w.Code, w.Body.String())
	}

	var resp struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error.Type != "invalid_request_error" {
		t.Errorf("expected type invalid_request_error, got %q", resp.Error.Type)
	}
	if resp.Error.Code != "model_not_found" {
		t.Errorf("expected code model_not_found, got %q", resp.Error.Code)
	}
}
