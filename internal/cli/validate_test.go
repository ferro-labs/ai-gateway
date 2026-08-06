package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return p
}

func TestRunValidate(t *testing.T) {
	const validYAML = "strategy:\n  mode: fallback\ntargets:\n  - virtual_key: openai\n"

	t.Run("valid config renders a summary", func(t *testing.T) {
		path := writeConfig(t, "config.yaml", validYAML)
		cmd, out := newHandlerCmd(t, "", "table")

		if err := runValidate(cmd, []string{path}); err != nil {
			t.Fatalf("runValidate: %v", err)
		}
		got := out.String()
		for _, want := range []string{"Config is valid", "fallback", "openai"} {
			if !strings.Contains(got, want) {
				t.Errorf("output missing %q:\n%s", want, got)
			}
		}
	})

	t.Run("json format prints the parsed config", func(t *testing.T) {
		path := writeConfig(t, "config.yaml", validYAML)
		cmd, out := newHandlerCmd(t, "", "json")

		if err := runValidate(cmd, []string{path}); err != nil {
			t.Fatalf("runValidate: %v", err)
		}
		if !strings.Contains(out.String(), "fallback") {
			t.Errorf("want JSON config, got:\n%s", out.String())
		}
	})

	t.Run("invalid config returns a validation error", func(t *testing.T) {
		path := writeConfig(t, "config.yaml", "strategy:\n  mode: bogus\ntargets:\n  - virtual_key: openai\n")
		cmd, _ := newHandlerCmd(t, "", "table")

		err := runValidate(cmd, []string{path})
		if err == nil || !strings.Contains(err.Error(), "validation failed") {
			t.Fatalf("want validation error, got %v", err)
		}
	})

	t.Run("missing file returns a load error", func(t *testing.T) {
		cmd, _ := newHandlerCmd(t, "", "table")

		err := runValidate(cmd, []string{filepath.Join(t.TempDir(), "nope.yaml")})
		if err == nil || !strings.Contains(err.Error(), "load config") {
			t.Fatalf("want load error, got %v", err)
		}
	})
}

// TestRunValidateCatchesUnknownReferences covers what validate can honestly
// promise: everything serve rejects that does not need the deployment. Names
// known at build time — provider IDs, plugin names, plugin stages — must
// resolve, because no environment can make a typo in one of them work; and a
// plugin listed at several stages under entries that disagree is refused here
// exactly as serve refuses it, because that answer is a property of the config
// alone.
//
// The cases with wantErr "" are the other half of the promise, and they matter
// as much: credentials are not checked, and neither are ${VAR} references.
// validate routinely runs in a pipeline with no secrets, so a target naming a
// real provider whose API key only exists in production — or a plugin whose
// config quotes an unset variable — is a config that works, and is reported as
// valid.
func TestRunValidateCatchesUnknownReferences(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		wantErr string
	}{
		{
			name:    "target naming a provider that does not exist",
			config:  "strategy:\n  mode: single\ntargets:\n  - virtual_key: not-a-real-provider\n",
			wantErr: "not-a-real-provider",
		},
		{
			name: "target naming a real provider with no credential in the environment",
			// openai is a real provider ID and OPENAI_API_KEY need not be set.
			// That must not be an error: validate makes no claim about secrets.
			config:  "strategy:\n  mode: single\ntargets:\n  - virtual_key: openai\n",
			wantErr: "",
		},
		{
			name: "unknown plugin name",
			config: "strategy:\n  mode: single\ntargets:\n  - virtual_key: openai\n" +
				"plugins:\n  - name: this-plugin-does-not-exist\n    type: guardrail\n" +
				"    stage: before_request\n    enabled: true\n",
			wantErr: "this-plugin-does-not-exist",
		},
		{
			name: "disabled plugin with an unknown name is not an error",
			// The gateway skips disabled entries when it builds the plugin
			// manager, so this config starts and serves. validate must not be
			// stricter than serve.
			config: "strategy:\n  mode: single\ntargets:\n  - virtual_key: openai\n" +
				"plugins:\n  - name: this-plugin-does-not-exist\n    type: guardrail\n" +
				"    stage: before_request\n    enabled: false\n",
			wantErr: "",
		},
		{
			name: "unknown plugin stage",
			config: "strategy:\n  mode: single\ntargets:\n  - virtual_key: openai\n" +
				"plugins:\n  - name: word-filter\n    type: guardrail\n" +
				"    stage: during_request\n    enabled: true\n",
			wantErr: "during_request",
		},
		{
			name: "one plugin at two stages with configs that disagree",
			// serve refuses this outright: the two entries become two instances
			// that never see each other's state, so the cache never serves a hit.
			// validate used to report it OK, which is the failure mode a
			// pre-flight gate exists to prevent.
			config: "strategy:\n  mode: single\ntargets:\n  - virtual_key: openai\n" +
				"plugins:\n  - name: response-cache\n    type: cache\n" +
				"    stage: before_request\n    enabled: true\n    config:\n      max_age: 300\n" +
				"  - name: response-cache\n    type: cache\n" +
				"    stage: after_request\n    enabled: true\n    config:\n      max_age: 301\n",
			wantErr: "2 different configs",
		},
		{
			name: "one plugin at two stages with configs that agree",
			config: "strategy:\n  mode: single\ntargets:\n  - virtual_key: openai\n" +
				"plugins:\n  - name: response-cache\n    type: cache\n" +
				"    stage: before_request\n    enabled: true\n    config:\n      max_age: 300\n" +
				"  - name: response-cache\n    type: cache\n" +
				"    stage: after_request\n    enabled: true\n    config:\n      max_age: 300\n",
			wantErr: "",
		},
		{
			name: "plugin rate that would blackhole every request",
			// A rate of zero rejects every request forever, and the gateway
			// starts and reports healthy while doing it. The plugin publishes
			// the rule (plugin.ConfigValidator); validate reads it here so the
			// answer arrives before the deploy rather than after.
			config: "strategy:\n  mode: single\ntargets:\n  - virtual_key: openai\n" +
				"plugins:\n  - name: rate-limit\n    type: guardrail\n" +
				"    stage: before_request\n    enabled: true\n    config:\n      requests_per_second: 0\n",
			wantErr: "requests_per_second must be > 0",
		},
		{
			name: "negative plugin rate",
			config: "strategy:\n  mode: single\ntargets:\n  - virtual_key: openai\n" +
				"plugins:\n  - name: rate-limit\n    type: guardrail\n" +
				"    stage: before_request\n    enabled: true\n    config:\n      requests_per_second: -1\n",
			wantErr: "requests_per_second must be > 0",
		},
		{
			name: "a plugin rate the gateway can actually serve",
			config: "strategy:\n  mode: single\ntargets:\n  - virtual_key: openai\n" +
				"plugins:\n  - name: rate-limit\n    type: guardrail\n" +
				"    stage: before_request\n    enabled: true\n    config:\n      requests_per_second: 100\n",
			wantErr: "",
		},
		{
			name: "disabled plugin with an unusable rate is not an error",
			// Same rule as the unknown-name case above: serve skips disabled
			// entries, so validate must not be stricter than serve.
			config: "strategy:\n  mode: single\ntargets:\n  - virtual_key: openai\n" +
				"plugins:\n  - name: rate-limit\n    type: guardrail\n" +
				"    stage: before_request\n    enabled: false\n    config:\n      requests_per_second: 0\n",
			wantErr: "",
		},
		{
			name: "plugin config referencing an environment variable that is not set",
			// The single most important thing validate must NOT do. envref
			// resolves ${VAR} at component construction, so requiring it here
			// would mean validate only passed where the secrets live — which is
			// the one place a pre-flight check is useless.
			config: "strategy:\n  mode: single\ntargets:\n  - virtual_key: openai\n" +
				"plugins:\n  - name: word-filter\n    type: guardrail\n" +
				"    stage: before_request\n    enabled: true\n    config:\n" +
				"      blocked_words:\n        - \"${FERRO_VALIDATE_NEVER_SET}\"\n",
			wantErr: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfig(t, "config.yaml", tt.config)
			cmd, _ := newHandlerCmd(t, "", "table")

			err := runValidate(cmd, []string{path})
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("runValidate: unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want an error mentioning %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// TestRunValidateAcceptsShippedConfigs guards the checks above against
// over-reach: the configs this repository ships must keep passing validate.
func TestRunValidateAcceptsShippedConfigs(t *testing.T) {
	names := []string{"config.example.yaml", "config.example.json"}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("..", "..", name)
			if _, err := os.Stat(path); err != nil {
				t.Skipf("%s not present: %v", path, err)
			}
			cmd, _ := newHandlerCmd(t, "", "table")
			if err := runValidate(cmd, []string{path}); err != nil {
				t.Fatalf("runValidate(%s): %v", name, err)
			}
		})
	}
}
