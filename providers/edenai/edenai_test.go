package edenai

import (
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestNewEdenAI(t *testing.T) {
	p, err := New("test-key", "")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if p.Name() != "edenai" {
		t.Errorf("Name() = %q, want edenai", p.Name())
	}
	if p.BaseURL() != defaultBaseURL {
		t.Errorf("BaseURL() = %q, want %q", p.BaseURL(), defaultBaseURL)
	}
}

func TestEdenAIProvider_SupportedModels(t *testing.T) {
	p, _ := New("test-key", "")
	models := p.SupportedModels()
	if len(models) == 0 {
		t.Fatal("SupportedModels() returned empty")
	}
	found := false
	for _, m := range models {
		if m == "anthropic/claude-sonnet-4-5" {
			found = true
		}
	}
	if !found {
		t.Error("anthropic/claude-sonnet-4-5 not found")
	}
}

func TestEdenAIProvider_SupportsModel(t *testing.T) {
	p, _ := New("test-key", "")
	if !p.SupportsModel("anthropic/claude-sonnet-4-5") {
		t.Error("expected anthropic/claude-sonnet-4-5 to be supported")
	}
	if !p.SupportsModel("custom/model") {
		t.Error("passthrough: expected all models to return true")
	}
}

func TestEdenAIProvider_Models(t *testing.T) {
	p, _ := New("test-key", "")
	for _, m := range p.Models() {
		if m.OwnedBy != "edenai" {
			t.Errorf("ModelInfo.OwnedBy = %q, want edenai", m.OwnedBy)
		}
	}
}

func TestEdenAIProvider_AuthHeaders(t *testing.T) {
	p, _ := New("test-key", "")
	if got := p.AuthHeaders()["Authorization"]; got != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", got)
	}
}

func TestEdenAIProvider_StreamInterface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.StreamProvider = p
}
