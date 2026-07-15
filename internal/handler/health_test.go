package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
			gw, err := newTestGateway(t, aigateway.Config{
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

func TestLivez(t *testing.T) {
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/livez", nil)
	w := httptest.NewRecorder()
	Livez().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", w.Code, http.StatusOK)
	}
	var payload struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode livez response: %v", err)
	}
	if payload.Status != "ok" {
		t.Fatalf("status = %q, want %q", payload.Status, "ok")
	}
}

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func TestReadyz(t *testing.T) {
	tests := []struct {
		name         string
		register     bool
		pingers      []Pinger
		wantCode     int
		wantStatus   string
		wantReasonIn string
	}{
		{
			name:       "ready with reachable stores",
			register:   true,
			pingers:    []Pinger{fakePinger{}, fakePinger{}},
			wantCode:   http.StatusOK,
			wantStatus: "ready",
		},
		{
			name:         "store unreachable",
			register:     true,
			pingers:      []Pinger{fakePinger{}, fakePinger{err: errors.New("connection refused")}},
			wantCode:     http.StatusServiceUnavailable,
			wantStatus:   "not_ready",
			wantReasonIn: "store unreachable",
		},
		{
			name:         "no ready providers",
			register:     false,
			pingers:      []Pinger{fakePinger{}},
			wantCode:     http.StatusServiceUnavailable,
			wantStatus:   "not_ready",
			wantReasonIn: "no ready providers",
		},
		{
			name:       "nil pinger is skipped",
			register:   true,
			pingers:    []Pinger{nil},
			wantCode:   http.StatusOK,
			wantStatus: "ready",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gw, err := newTestGateway(t, aigateway.Config{
				Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
				Targets:  []aigateway.Target{{VirtualKey: "health-provider"}},
			})

			if err != nil {
				t.Fatalf("New gateway: %v", err)
			}
			if tt.register {
				gw.RegisterProvider(healthProvider{})
			}

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil)
			w := httptest.NewRecorder()
			Readyz(gw, tt.pingers...).ServeHTTP(w, req)

			if w.Code != tt.wantCode {
				t.Fatalf("status code = %d, want %d: %s", w.Code, tt.wantCode, w.Body.String())
			}
			var payload struct {
				Status string `json:"status"`
				Reason string `json:"reason"`
			}
			if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
				t.Fatalf("decode readyz response: %v", err)
			}
			if payload.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", payload.Status, tt.wantStatus)
			}
			if tt.wantReasonIn != "" && !strings.Contains(payload.Reason, tt.wantReasonIn) {
				t.Fatalf("reason = %q, want substring %q", payload.Reason, tt.wantReasonIn)
			}
		})
	}
}

func TestReadyzNilGateway(t *testing.T) {
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	Readyz(nil).ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status code = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}

func TestReadyzDoesNotLeakStoreErrorDetail(t *testing.T) {
	gw, err := newTestGateway(t, aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: "health-provider"}},
	})

	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}
	gw.RegisterProvider(healthProvider{})

	//nolint:gosec // G101: a fake DSN; the point of the test is that /readyz never echoes it
	const secret = "postgres://admin:hunter2@db.internal:5432/gateway"
	pinger := fakePinger{err: errors.New("dial tcp: " + secret + ": connection refused")}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	Readyz(gw, pinger).ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status code = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
	if strings.Contains(w.Body.String(), secret) {
		t.Fatalf("response body leaked store error detail: %s", w.Body.String())
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
