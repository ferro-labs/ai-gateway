// Package config resolves where ferro connects and with which credential.
// Precedence — path: --config > FERRO_CONFIG > DefaultPath()
//
//	URL:  --gateway-url > FERRO_URL > profile.url > http://localhost:8080
//	key:  FERRO_API_KEY > profile.api_key_env deref > MASTER_KEY > ""
//
// ferro stores no gateway data; this file holds connection profiles only.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// DefaultURL is where ferro looks when nothing else says otherwise.
const DefaultURL = "http://localhost:8080"

// The environment variables Resolve reads, and the KeySource label it reports
// for each source. Building the labels from the variable names is what keeps
// the two spellings from drifting: a doctor screen naming a variable nothing
// reads is worse than no label at all.
const (
	envURL       = "FERRO_URL"
	envFerroKey  = "FERRO_API_KEY"
	envMasterKey = "MASTER_KEY"

	keySourceFerroEnv  = "env:" + envFerroKey
	keySourceMasterEnv = "env:" + envMasterKey
	keySourceProfile   = "profile:"
)

// Profile is one named connection. It holds no credential: api_key_env names
// an environment variable, so the config file never carries a secret.
type Profile struct {
	Name      string `yaml:"name"`
	URL       string `yaml:"url"`
	APIKeyEnv string `yaml:"api_key_env,omitempty"`
}

// File is the on-disk ferro config: connection profiles and nothing else.
type File struct {
	CurrentProfile string    `yaml:"current_profile,omitempty"`
	Profiles       []Profile `yaml:"profiles,omitempty"`
}

// Resolved is what the rest of ferro consumes. KeySource is for doctor-style
// display ("env:FERRO_API_KEY", "profile:PROD_KEY", "env:MASTER_KEY").
type Resolved struct {
	ProfileName string
	URL         string
	APIKey      string
	KeySource   string
}

// EnvConfigPath is the environment variable that overrides DefaultPath.
// --config on the CLI outranks it in turn (see the precedence note above). A
// config path names a filesystem location, never a credential, so unlike
// FERRO_API_KEY it carries nothing that must not appear in a shell history.
const EnvConfigPath = "FERRO_CONFIG"

// DefaultPath is the config location, empty when the OS reports no config dir.
func DefaultPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "ferro", "config.yaml")
}

// Load reads the config file. A missing file is not an error -- ferro works
// with flags and environment alone.
func Load(path string) (File, error) {
	var f File
	if path == "" {
		return f, nil
	}
	// Normalise before use so the file that is read and the file a parse error
	// names are spelled the same way, whatever lexical noise (`a/../b`, a
	// trailing slash) the flag or FERRO_CONFIG carried.
	clean := filepath.Clean(path)
	b, err := os.ReadFile(clean)
	if os.IsNotExist(err) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := yaml.Unmarshal(b, &f); err != nil {
		return f, fmt.Errorf("parse %s: %w", clean, err)
	}
	return f, nil
}

// Resolve applies the precedence documented on this package to produce the
// connection ferro will use.
func Resolve(f File, flagURL, flagProfile string, getenv func(string) string) (Resolved, error) {
	r := Resolved{URL: DefaultURL}

	name := flagProfile
	if name == "" {
		name = f.CurrentProfile
	}
	var prof *Profile
	if name != "" {
		for i := range f.Profiles {
			if f.Profiles[i].Name == name {
				prof = &f.Profiles[i]
				break
			}
		}
		if prof == nil {
			names := make([]string, 0, len(f.Profiles))
			for _, p := range f.Profiles {
				names = append(names, p.Name)
			}
			sort.Strings(names)
			if len(names) == 0 {
				return r, fmt.Errorf("unknown profile %q (no profiles are defined)", name)
			}
			return r, fmt.Errorf("unknown profile %q (valid: %s)", name, strings.Join(names, ", "))
		}
	}
	if prof != nil {
		r.ProfileName = prof.Name
		if prof.URL != "" {
			r.URL = prof.URL
		}
	}
	if v := getenv(envURL); v != "" {
		r.URL = v
	}
	if flagURL != "" {
		r.URL = flagURL
	}

	switch {
	case getenv(envFerroKey) != "":
		r.APIKey, r.KeySource = getenv(envFerroKey), keySourceFerroEnv
	case prof != nil && prof.APIKeyEnv != "" && getenv(prof.APIKeyEnv) != "":
		r.APIKey, r.KeySource = getenv(prof.APIKeyEnv), keySourceProfile+prof.APIKeyEnv
	case getenv(envMasterKey) != "":
		r.APIKey, r.KeySource = getenv(envMasterKey), keySourceMasterEnv
	}
	return r, nil
}
