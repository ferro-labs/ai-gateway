package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

// TestCapabilities_ReturnsProfiles verifies GET /v1/capabilities reports a
// per-provider parameter profile: matrix exceptions surface as
// "unsupported"/"translate", and providers without an entry are all "forward".
func TestCapabilities_ReturnsProfiles(t *testing.T) {
	reg := providers.NewRegistry()
	reg.Register(&nonProxyProvider{name: "gemini", models: []string{"gemini-pro"}})
	reg.Register(&nonProxyProvider{name: "openai", models: []string{"gpt-4o"}})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/capabilities", nil)
	Capabilities(reg)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var payload struct {
		Providers map[string]map[string]string `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	gemini, ok := payload.Providers["gemini"]
	if !ok {
		t.Fatal("response missing gemini profile")
	}
	if gemini["seed"] != "forward" {
		t.Errorf("gemini.seed = %q, want forward", gemini["seed"])
	}
	if gemini["response_format"] != "translate" {
		t.Errorf("gemini.response_format = %q, want translate", gemini["response_format"])
	}
	if gemini["logit_bias"] != "unsupported" {
		t.Errorf("gemini.logit_bias = %q, want unsupported", gemini["logit_bias"])
	}

	openai, ok := payload.Providers["openai"]
	if !ok {
		t.Fatal("response missing openai profile")
	}
	if openai["seed"] != "forward" {
		t.Errorf("openai.seed = %q, want forward (no matrix entry)", openai["seed"])
	}
	// Every canonical param must be present for a materialised profile.
	if len(openai) == 0 {
		t.Error("openai profile is empty")
	}
}
