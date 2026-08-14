package command

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func execute(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRoot()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errb.String(), err
}

func TestVersionCommand(t *testing.T) {
	stdout, _, err := execute(t, "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.Contains(stdout, "ferro") || !strings.Contains(stdout, "dev") {
		t.Fatalf("want binary name + version on stdout, got %q", stdout)
	}
}

func TestUnknownCommandFails(t *testing.T) {
	_, _, err := execute(t, "definitely-not-a-verb")
	if err == nil {
		t.Fatal("unknown command must error (exit 1)")
	}
}

func TestBareCommandRequiresTerminalInput(t *testing.T) {
	oldStdin, oldStdout := stdinIsTTY, stdoutIsTTY
	stdinIsTTY = func() bool { return false }
	stdoutIsTTY = func() bool { return true }
	t.Cleanup(func() { stdinIsTTY, stdoutIsTTY = oldStdin, oldStdout })
	_, _, err := execute(t)
	if err == nil || !strings.Contains(err.Error(), "input and output") {
		t.Fatalf("bare ferro with redirected stdin must refuse the TUI: %v", err)
	}
}

func TestRootDoesNotAcceptAPIKeyInArguments(t *testing.T) {
	if flag := NewRoot().PersistentFlags().Lookup("api-key"); flag != nil {
		t.Fatal("bearer credentials must not be accepted through process arguments")
	}
}

func TestCompletionSkipsMalformedConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "ferro"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ferro", "config.yaml"), []byte(": bad: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := NewRoot()
	root.AddCommand(&cobra.Command{Use: "completion", Run: func(*cobra.Command, []string) {}})
	root.SetArgs([]string{"completion"})
	if err := root.Execute(); err != nil {
		t.Fatalf("static completion must not parse connection config: %v", err)
	}
}

// The version command must work with no gateway and no credentials, so it
// cannot be gated behind the connection setup every other verb needs.
func TestVersionNeedsNoGateway(t *testing.T) {
	t.Setenv("FERRO_URL", "http://127.0.0.1:1")
	if _, _, err := execute(t, "version"); err != nil {
		t.Fatalf("version must not touch the network: %v", err)
	}
}

// version opts out of connection setup entirely: it must work with no config
// file, no credential, and no reachable gateway.
func TestVersionSkipsConnectionSetup(t *testing.T) {
	t.Setenv("FERRO_URL", "http://127.0.0.1:1")
	if _, _, err := execute(t, "version", "--format", "xml"); err != nil {
		t.Fatalf("version must bypass flag validation and client setup: %v", err)
	}
}

// Connection setup runs for every verb except version. Probed with a stub
// command so this stays a test of the wiring rather than of whichever verb
// happens to exist.
func withProbe(t *testing.T, args ...string) (*runtimeDeps, error) {
	t.Helper()
	root := NewRoot()
	var got *runtimeDeps
	root.AddCommand(&cobra.Command{
		Use: "probe",
		RunE: func(cmd *cobra.Command, _ []string) error {
			got = deps(cmd)
			return nil
		},
	})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs(append([]string{"probe"}, args...))
	// Execute first, then read got. Go orders function calls in a return
	// statement left to right, but it does not order a variable read against
	// a call beside it — and got is assigned inside this one, by RunE. As
	// `return got, root.Execute()` the read may legally happen first, handing
	// every caller a nil deps and passing anyway.
	err := root.Execute()
	return got, err
}

func TestBadFormatRejectedWithGuidance(t *testing.T) {
	_, err := withProbe(t, "--format", "xml")
	if err == nil || !strings.Contains(err.Error(), "table") {
		t.Fatalf("bad --format must name the valid values, got %v", err)
	}
}

func TestDepsBuiltFromFlagsAndEnv(t *testing.T) {
	t.Setenv("FERRO_API_KEY", "fgw_from_env")
	d, err := withProbe(t, "--gateway-url", "http://gw.example:9000")
	if err != nil {
		t.Fatal(err)
	}
	if d == nil {
		t.Fatal("every non-version verb must receive runtime deps")
	}
	if d.Resolved.URL != "http://gw.example:9000" {
		t.Fatalf("flag must win for URL, got %q", d.Resolved.URL)
	}
	if d.Resolved.KeySource != "env:FERRO_API_KEY" {
		t.Fatalf("key source: %q", d.Resolved.KeySource)
	}
	if d.Client == nil || d.Printer == nil {
		t.Fatal("deps must carry a client and a printer")
	}
}

func writeConfigFile(t *testing.T, dir, name, profile, url string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	body := "current_profile: " + profile + "\nprofiles:\n  - name: " + profile + "\n    url: " + url + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// --config names a config file DefaultPath would never find, so PersistentPreRunE
// must resolve the connection from it rather than from the OS default location.
func TestConfigFlagOverridesDefaultPath(t *testing.T) {
	path := writeConfigFile(t, t.TempDir(), "custom.yaml", "from-flag", "http://from-flag:1")
	d, err := withProbe(t, "--config", path)
	if err != nil {
		t.Fatal(err)
	}
	if d.Resolved.URL != "http://from-flag:1" {
		t.Fatalf("--config must load the named file, got %+v", d.Resolved)
	}
}

// FERRO_CONFIG is the env-var override the config package's own doc comment
// promises; without wiring it in, no flag or env var could ever change the
// path config.Load reads, no matter what the comment said.
func TestFerroConfigEnvOverridesDefaultPath(t *testing.T) {
	path := writeConfigFile(t, t.TempDir(), "env.yaml", "from-env", "http://from-env:2")
	t.Setenv("FERRO_CONFIG", path)
	d, err := withProbe(t)
	if err != nil {
		t.Fatal(err)
	}
	if d.Resolved.URL != "http://from-env:2" {
		t.Fatalf("FERRO_CONFIG must override the default path, got %+v", d.Resolved)
	}
}

func TestConfigFlagBeatsFerroConfigEnv(t *testing.T) {
	dir := t.TempDir()
	envPath := writeConfigFile(t, dir, "env.yaml", "from-env", "http://from-env:1")
	flagPath := writeConfigFile(t, dir, "flag.yaml", "from-flag", "http://from-flag:2")
	t.Setenv("FERRO_CONFIG", envPath)
	d, err := withProbe(t, "--config", flagPath)
	if err != nil {
		t.Fatal(err)
	}
	if d.Resolved.URL != "http://from-flag:2" {
		t.Fatalf("--config must win over FERRO_CONFIG, got %+v", d.Resolved)
	}
}

// Regression for the Printer's colour decision being taken from stdout while
// its narrative (OK/Warn/Fail) is always written to stderr: with a terminal
// stdout and a redirected stderr -- `ferro status 2> run.log` -- the old
// stdout-only check would turn colour on and leak ANSI escapes into the file.
// The decision must follow the stream the Printer actually writes to.
func TestPrinterColorFollowsStderrNotStdout(t *testing.T) {
	oldIsTTY := isTTY
	// stdout is a terminal; stderr is not -- the shape `2> run.log` takes.
	isTTY = func(f *os.File) bool { return f == os.Stdout }
	t.Cleanup(func() { isTTY = oldIsTTY })

	d, err := withProbe(t)
	if err != nil {
		t.Fatal(err)
	}
	if d.Printer.Color {
		t.Fatal("colour must follow stderr's terminal state, not stdout's")
	}
}
