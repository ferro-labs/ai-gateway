package providers

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestVertexAIBuild_NoExplicitKeyReachesADC verifies the vertex-ai factory no
// longer rejects the no-api-key / no-service-account config before New() runs,
// so the Application Default Credentials (workload-identity) path is reachable.
// With ADC forced unavailable, the error must come from New()'s ADC-aware
// validation, not a pre-flight factory guard.
func TestVertexAIBuild_NoExplicitKeyReachesADC(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "none.json"))

	entry, ok := GetProviderEntry(NameVertexAI)
	if !ok {
		t.Fatal("vertex-ai provider entry not found")
	}

	_, err := entry.Build(ProviderConfig{
		CfgKeyProjectID: "demo",
		CfgKeyRegion:    "us-central1",
	})
	if err == nil {
		// ADC happened to be available on this host; the guard relaxation still
		// holds (no pre-flight rejection), which is the point of the fix.
		return
	}
	if strings.Contains(err.Error(), "either api_key") {
		t.Fatalf("factory still pre-rejects the no-key case, defeating ADC: %v", err)
	}
	if !strings.Contains(err.Error(), "application default credentials") {
		t.Fatalf("error = %v, want New()'s ADC-aware validation error", err)
	}
}
