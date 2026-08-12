package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestResolvePrecedence(t *testing.T) {
	file := File{
		CurrentProfile: "prod",
		Profiles: []Profile{
			{Name: "prod", URL: "https://gw.example.com", APIKeyEnv: "PROD_KEY"},
			{Name: "local", URL: "http://localhost:9999"},
		},
	}
	cases := []struct {
		name             string
		flagURL          string
		flagProfile      string
		envv             map[string]string
		wantURL, wantKey string
		wantSource       string
	}{
		{name: "URL flag and key environment beat profile", flagURL: "http://flag:1",
			envv:    map[string]string{"FERRO_URL": "http://env:1", "FERRO_API_KEY": "fgw_env"},
			wantURL: "http://flag:1", wantKey: "fgw_env", wantSource: "env:FERRO_API_KEY"},
		{name: "env beats profile",
			envv:    map[string]string{"FERRO_URL": "http://env:1", "FERRO_API_KEY": "fgw_env", "PROD_KEY": "fgw_prod"},
			wantURL: "http://env:1", wantKey: "fgw_env", wantSource: "env:FERRO_API_KEY"},
		{name: "profile url + key env deref",
			envv:    map[string]string{"PROD_KEY": "fgw_prod"},
			wantURL: "https://gw.example.com", wantKey: "fgw_prod", wantSource: "profile:PROD_KEY"},
		{name: "MASTER_KEY is the last key fallback",
			flagProfile: "local", envv: map[string]string{"MASTER_KEY": "fgw_master"},
			wantURL: "http://localhost:9999", wantKey: "fgw_master", wantSource: "env:MASTER_KEY"},
		{name: "defaults", flagProfile: "none-selected", envv: map[string]string{},
			flagURL: "", wantURL: "http://localhost:8080", wantKey: "", wantSource: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := file
			if tc.flagProfile == "none-selected" {
				f = File{} // no profiles at all
				tc.flagProfile = ""
			}
			r, err := Resolve(f, tc.flagURL, tc.flagProfile, env(tc.envv))
			if err != nil {
				t.Fatal(err)
			}
			if r.URL != tc.wantURL || r.APIKey != tc.wantKey || r.KeySource != tc.wantSource {
				t.Fatalf("got url=%q key=%q src=%q", r.URL, r.APIKey, r.KeySource)
			}
		})
	}
}

func TestResolveUnknownProfileErrors(t *testing.T) {
	_, err := Resolve(File{Profiles: []Profile{{Name: "a"}}}, "", "nope", env(nil))
	if err == nil {
		t.Fatal("unknown --profile must error and list valid names")
	}
}

func TestResolveStaleCurrentProfileErrors(t *testing.T) {
	_, err := Resolve(File{CurrentProfile: "removed", Profiles: []Profile{{Name: "prod"}}}, "", "", env(nil))
	if err == nil || !strings.Contains(err.Error(), `unknown profile "removed"`) {
		t.Fatalf("a stale current_profile must not silently connect to localhost: %v", err)
	}
	_, err = Resolve(File{CurrentProfile: "removed"}, "", "", env(nil))
	if err == nil || !strings.Contains(err.Error(), "no profiles are defined") {
		t.Fatalf("an empty profile list needs a clear error: %v", err)
	}
}

func TestLoadMissingFileIsZero(t *testing.T) {
	f, err := Load("/definitely/not/here.yaml")
	if err != nil || len(f.Profiles) != 0 {
		t.Fatalf("missing file must be zero value, got %+v, %v", f, err)
	}
}

func TestLoadMalformedYAMLNamesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("profiles: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("malformed YAML must fail and name its file: %v", err)
	}
}
