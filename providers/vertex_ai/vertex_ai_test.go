package vertexai

import (
	"path/filepath"
	"testing"
)

const testAPIKey = "test-key"

func TestNewVertexAI_APIKeyMode(t *testing.T) {
	p, err := New(Options{
		ProjectID: "demo-project",
		Region:    "us-central1",
		APIKey:    testAPIKey,
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if p.Name() != "vertex-ai" {
		t.Errorf("Name() = %q, want vertex-ai", p.Name())
	}
	if p.BaseURL() == "" {
		t.Error("BaseURL() should not be empty")
	}
}

func TestNewVertexAI_RequiresProjectID(t *testing.T) {
	_, err := New(Options{Region: "us-central1", APIKey: testAPIKey})
	if err == nil {
		t.Fatal("expected error for missing project_id")
	}
}

func TestNewVertexAI_RequiresRegion(t *testing.T) {
	_, err := New(Options{ProjectID: "demo-project", APIKey: testAPIKey})
	if err == nil {
		t.Fatal("expected error for missing region")
	}
}

func TestNewVertexAI_RequiresAuth(t *testing.T) {
	// Force Application Default Credentials discovery to fail deterministically
	// (bogus GOOGLE_APPLICATION_CREDENTIALS path) so the "no auth available"
	// error path is exercised regardless of the host's ambient gcloud/metadata
	// credentials.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "does-not-exist.json"))
	_, err := New(Options{ProjectID: "demo-project", Region: "us-central1"})
	if err == nil {
		t.Fatal("expected error when no api key, service account JSON, or ADC is available")
	}
}
