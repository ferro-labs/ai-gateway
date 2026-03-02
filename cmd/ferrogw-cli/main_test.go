package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/version"
	"github.com/ferro-labs/ai-gateway/plugin"
)

// captureOutput captures stdout and stderr during function execution.
func captureOutput(f func()) (stdout, stderr string) {
	oldStdout := os.Stdout
	oldStderr := os.Stderr

	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()

	os.Stdout = wOut
	os.Stderr = wErr

	f()

	wOut.Close()
	wErr.Close()

	var bufOut, bufErr bytes.Buffer
	io.Copy(&bufOut, rOut)
	io.Copy(&bufErr, rErr)

	os.Stdout = oldStdout
	os.Stderr = oldStderr

	return bufOut.String(), bufErr.String()
}

func TestCmdHelp(t *testing.T) {
	stdout, _ := captureOutput(func() {
		os.Args = []string{"ferrogw-cli", "help"}
		// Can't call main() directly due to os.Exit, test the output pattern
	})

	// Since main() calls os.Exit, we test the usage string directly
	if !strings.Contains(usage, "ferrogw-cli") {
		t.Error("usage should contain 'ferrogw-cli'")
	}
	if !strings.Contains(usage, "validate") {
		t.Error("usage should contain 'validate' command")
	}
	if !strings.Contains(usage, "plugins") {
		t.Error("usage should contain 'plugins' command")
	}
	if !strings.Contains(usage, "version") {
		t.Error("usage should contain 'version' command")
	}

	_ = stdout // Used to avoid unused variable warning
}

func TestCmdPlugins_ListsAllPlugins(t *testing.T) {
	names := plugin.RegisteredPlugins()

	expectedPlugins := []string{
		"max-token",
		"word-filter",
		"response-cache",
		"request-logger",
		"rate-limit",
	}

	for _, expected := range expectedPlugins {
		found := false
		for _, name := range names {
			if name == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected plugin %q to be registered", expected)
		}
	}
}

func TestCmdVersion_NonEmpty(t *testing.T) {
	v := version.String()
	if v == "" {
		t.Error("version string should not be empty")
	}
}

func TestUsage_ContainsAllCommands(t *testing.T) {
	commands := []string{"validate", "plugins", "version", "help"}
	for _, cmd := range commands {
		if !strings.Contains(usage, cmd) {
			t.Errorf("usage should contain command %q", cmd)
		}
	}
}
