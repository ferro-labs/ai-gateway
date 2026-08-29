package run

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// runCLI executes the real command tree with args, recording whether the server
// entrypoint was reached. Both writers go to one buffer because several of these
// assertions are about what lands anywhere at all.
func runCLI(t *testing.T, args ...string) (out *bytes.Buffer, served *bool, err error) {
	t.Helper()
	out = &bytes.Buffer{}
	ran := false
	root := newRootCmd(func() error { ran = true; return nil })
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs(args)
	err = root.Execute()
	return out, &ran, err
}

// OPS-005. The root is runnable and is also the parent of every subcommand, so
// an unmatched first token must not fall through and boot a gateway — a deploy
// gate spelled slightly wrong would "pass" by starting a server on :8080.
//
// Cobra already refuses it, but only because the root leaves Args nil: Find
// applies legacyArgs to a command whose Args is nil, and legacyArgs rejects an
// unknown token for a root that has subcommands. Give the root ANY Args policy
// and that path is skipped — cobra.NoArgs still rejects but drops the
// "Did you mean this?" line, and cobra.ArbitraryArgs starts the server. The
// suggestion is asserted here because it is the cheap tell that the right
// mechanism is doing the rejecting.
func TestRootRejectsUnknownSubcommand(t *testing.T) {
	out, served, err := runCLI(t, "valdate", "config.yaml")

	if err == nil {
		t.Fatal("an unknown subcommand must exit non-zero")
	}
	if *served {
		t.Fatal("an unknown subcommand started the server instead of failing")
	}
	if !strings.Contains(out.String(), "valdate") {
		t.Errorf("the error must name the token that was not understood:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "validate") {
		t.Errorf("the error must suggest the command that was meant:\n%s", out.String())
	}
}

// The bare `ferrogw` is the documented way to start the server, so the guard
// above must not have cost it.
func TestBareRootStillServes(t *testing.T) {
	_, served, err := runCLI(t)

	if err != nil {
		t.Fatalf("bare ferrogw must start the server, got error: %v", err)
	}
	if !*served {
		t.Fatal("bare ferrogw did not start the server")
	}
}

func TestServeRejectsExtraArguments(t *testing.T) {
	_, served, err := runCLI(t, "serve", "config.yaml")

	if err == nil {
		t.Fatal("`serve` takes no arguments; a stray one must not be swallowed")
	}
	if *served {
		t.Fatal("`serve` ran despite an argument it does not accept")
	}
}

// OPS-011(c). Cobra prints the whole usage block after a failing command — to
// STDOUT, while the error goes to stderr — so the one line saying what went
// wrong was buried under a flag list, and anything reading stdout got the flag
// list instead of nothing.
func TestCommandErrorPrintsNoUsageBlock(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	out, _, err := runCLI(t, "validate", missing)

	if err == nil {
		t.Fatal("validate must fail on a config file that does not exist")
	}
	got := out.String()
	if strings.Contains(got, "Usage:") || strings.Contains(got, "Flags:") {
		t.Errorf("a command error must not drag the usage block with it:\n%s", got)
	}
	if !strings.Contains(got, "load config") {
		t.Errorf("output does not carry the actual error:\n%s", got)
	}
}

func TestServeErrorIsNotPrintedTwice(t *testing.T) {
	out := &bytes.Buffer{}
	root := newRootCmd(func() error { return errors.New("startup failed") })
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"serve"})

	if err := root.Execute(); err == nil {
		t.Fatal("serve must return its startup error")
	}
	if strings.Contains(out.String(), "startup failed") {
		t.Fatalf("Cobra printed an error the startup path already logged: %s", out.String())
	}
}

// A nil Args is cobra's ArbitraryArgs: the command accepts tokens it will never
// read, which is how a mistyped invocation becomes a silent no-op. This walks
// the root's own children — the commands this binary mounts — so one added
// later is covered without editing this test. It stops at containers rather
// than recursing: `admin`'s leaves are declared in their own file and are that
// file's contract to state.
func TestMountedCommandsDeclareAnArgumentPolicy(t *testing.T) {
	root := newRootCmd(func() error { return nil })
	// The root is the exception, and the opposite way round: see
	// TestRootRejectsUnknownSubcommand.
	if root.Args != nil {
		t.Error("the root must leave Args nil so cobra's legacyArgs rejects an unknown subcommand with a suggestion")
	}
	for _, cmd := range root.Commands() {
		if cmd.Name() == "help" || cmd.Name() == "completion" || cmd.HasSubCommands() {
			continue
		}
		if cmd.Args == nil {
			t.Errorf("command %q declares no Args policy", cmd.Name())
		}
	}
}
