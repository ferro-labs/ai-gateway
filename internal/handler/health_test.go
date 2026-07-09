package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/providers"
)

func TestHealthStatusCodes(t *testing.T) {
	tests := []struct {
		name       string
		register   bool
		wantStatus string
		wantCode   int
	}{
		{name: "healthy", register: true, wantStatus: "ok", wantCode: http.StatusOK},
		{name: "no providers", register: false, wantStatus: "no_providers", wantCode: http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gw, err := aigateway.New(aigateway.Config{
				Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
				Targets:  []aigateway.Target{{VirtualKey: "health-provider"}},
			})
			if err != nil {
				t.Fatalf("New gateway: %v", err)
			}
			if tt.register {
				gw.RegisterProvider(healthProvider{})
			}

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", nil)
			w := httptest.NewRecorder()
			Health(gw).ServeHTTP(w, req)

			if w.Code != tt.wantCode {
				t.Fatalf("status code = %d, want %d: %s", w.Code, tt.wantCode, w.Body.String())
			}
			var payload struct {
				Status string `json:"status"`
			}
			if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
				t.Fatalf("decode health response: %v", err)
			}
			if payload.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", payload.Status, tt.wantStatus)
			}
		})
	}
}

type healthProvider struct{}

func (healthProvider) Name() string              { return "health-provider" }
func (healthProvider) SupportedModels() []string { return []string{"health-model"} }
func (healthProvider) SupportsModel(model string) bool {
	return model == "health-model"
}
func (healthProvider) Models() []providers.ModelInfo {
	return []providers.ModelInfo{{ID: "health-model", Object: "model", OwnedBy: "health-provider"}}
}
func (healthProvider) Complete(context.Context, providers.Request) (*providers.Response, error) {
	return nil, nil
}
