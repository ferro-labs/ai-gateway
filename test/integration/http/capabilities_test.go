//go:build integration
// +build integration

package http_test

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"sort"
	"testing"
)

// capabilitiesSupport is the closed set of values a capability profile entry may
// hold. Anything else means the matrix serialized something no client can read.
var capabilitiesSupport = []string{"forward", "translate", "unsupported"}

// getCapabilities fetches and decodes GET /v1/capabilities through the wired
// router with a valid credential.
func getCapabilities(t *testing.T, env *testEnv) map[string]map[string]string {
	t.Helper()

	req := newTestRequest(t, http.MethodGet, env.Server.URL+"/v1/capabilities", nil)
	req.Header.Set("Authorization", "Bearer "+testMasterKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/capabilities: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var payload struct {
		Providers map[string]map[string]string `json:"providers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return payload.Providers
}

// The capability profile is served for every provider a configured target
// names, and each parameter carries one of the three defined support values.
func TestCapabilities_ServesProfilePerServedProvider(t *testing.T) {
	env := newTestServer(t)

	providers := getCapabilities(t, env)
	profile, ok := providers["stub"]
	if !ok {
		t.Fatalf("providers = %v, want an entry for the configured target %q", providers, "stub")
	}
	if len(profile) == 0 {
		t.Fatal("stub profile is empty; every parameter in the matrix should be reported")
	}
	for param, support := range profile {
		if !slices.Contains(capabilitiesSupport, support) {
			t.Errorf("%s = %q, want one of %v", param, support, capabilitiesSupport)
		}
	}
}

// The documented invariant: /v1/capabilities answers from the same served set
// as /v1/models, so a client cannot be told a provider forwards a parameter and
// then get a 404 from every surface that would carry it.
func TestCapabilities_ProviderSetMatchesModelsOwners(t *testing.T) {
	env := newTestServer(t)

	profiles := getCapabilities(t, env)
	capProviders := make([]string, 0, len(profiles))
	for name := range profiles {
		capProviders = append(capProviders, name)
	}

	req := newTestRequest(t, http.MethodGet, env.Server.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/models: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	var models struct {
		Data []struct {
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&models); err != nil {
		t.Fatalf("decode models: %v", err)
	}

	owners := make([]string, 0, len(models.Data))
	for _, m := range models.Data {
		if !slices.Contains(owners, m.OwnedBy) {
			owners = append(owners, m.OwnedBy)
		}
	}

	sort.Strings(capProviders)
	sort.Strings(owners)
	if !slices.Equal(capProviders, owners) {
		t.Errorf("capabilities providers %v != /v1/models owners %v", capProviders, owners)
	}
}

// The route sits behind the same auth as its siblings; an unauthenticated
// caller must not be able to enumerate which providers this instance serves.
func TestCapabilities_RequiresAuth(t *testing.T) {
	env := newTestServer(t)

	req := newTestRequest(t, http.MethodGet, env.Server.URL+"/v1/capabilities", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/capabilities: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}
