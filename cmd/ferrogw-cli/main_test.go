package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/version"
)

func TestExecute(t *testing.T) {
	tests := []struct {
		name           string
		args           []string
		expectedExit   int
		expectedStdout string
		expectedStderr string
	}{
		{
			name:           "no arguments",
			args:           []string{"ferrogw-cli"},
			expectedExit:   0,
			expectedStdout: "Usage:",
			expectedStderr: "",
		},
		{
			name:           "help command",
			args:           []string{"ferrogw-cli", "help"},
			expectedExit:   0,
			expectedStdout: "Usage:",
			expectedStderr: "",
		},
		{
			name:           "-h flag",
			args:           []string{"ferrogw-cli", "-h"},
			expectedExit:   0,
			expectedStdout: "Usage:",
			expectedStderr: "",
		},
		{
			name:           "--help flag",
			args:           []string{"ferrogw-cli", "--help"},
			expectedExit:   0,
			expectedStdout: "Usage:",
			expectedStderr: "",
		},
		{
			name:           "version command",
			args:           []string{"ferrogw-cli", "version"},
			expectedExit:   0,
			expectedStdout: "ferrogw-cli " + version.String(),
			expectedStderr: "",
		},
		{
			name:           "plugins command",
			args:           []string{"ferrogw-cli", "plugins"},
			expectedExit:   0,
			expectedStdout: "NAME",
			expectedStderr: "",
		},
		{
			name:           "unknown command",
			args:           []string{"ferrogw-cli", "unknown"},
			expectedExit:   1,
			expectedStdout: "",
			expectedStderr: "unknown command",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}
			exitCode := execute(tt.args, stdout, stderr)

			if exitCode != tt.expectedExit {
				t.Errorf("expected exit code %d, got %d", tt.expectedExit, exitCode)
			}

			if !strings.Contains(stdout.String(), tt.expectedStdout) {
				t.Errorf("expected stdout to contain %q, got %q", tt.expectedStdout, stdout.String())
			}

			if !strings.Contains(stderr.String(), tt.expectedStderr) {
				t.Errorf("expected stderr to contain %q, got %q", tt.expectedStderr, stderr.String())
			}
		})
	}
}

func TestPluginsListsBuiltinNames(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	exitCode := execute([]string{"ferrogw-cli", "plugins"}, stdout, stderr)

	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}

	out := stdout.String()
	knownPlugins := []string{"response-cache", "request-logger", "max-token", "word-filter"}
	for _, name := range knownPlugins {
		if !strings.Contains(out, name) {
			t.Errorf("expected plugins output to contain built-in plugin %q, got %q", name, out)
		}
	}
}
