// Package command holds ferro's scriptable Cobra surface. It owns the
// stdout/stderr split, output formats, and exit codes; it holds no terminal
// screen state. Bare `ferro` hands off to internal/tui.
package command

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ferro-labs/ai-gateway/cli/internal/api"
	"github.com/ferro-labs/ai-gateway/cli/internal/config"
	"github.com/ferro-labs/ai-gateway/cli/internal/tui"
	"github.com/ferro-labs/ai-gateway/cli/internal/tui/theme"
	"github.com/ferro-labs/ai-gateway/cli/internal/version"
)

type depsKey struct{}

// verbList is the one spelling of the listing verb. Every noun that can be
// listed spells it this way, so an operator who has typed `keys list` can guess
// `logs list` rather than discover it is `logs ls`.
const verbList = "list"

// runtimeDeps is what every verb needs and none of them should assemble for
// itself: the resolved connection, an output printer honouring the global
// flags, and a client pointed at that connection.
type runtimeDeps struct {
	Resolved config.Resolved
	Printer  *Printer
	Client   *api.Client
}

// deps returns the runtime wiring built by PersistentPreRunE. It is nil only
// for commands that opt out of setup (version), which never call it.
func deps(cmd *cobra.Command) *runtimeDeps {
	d, _ := cmd.Context().Value(depsKey{}).(*runtimeDeps)
	return d
}

// printStructured renders a value for --format json|yaml. Table-formatted
// commands that have no sensible column layout fall back to JSON rather than
// inventing one.
func (d *runtimeDeps) printStructured(v any) error {
	if d.Printer.Format == FormatYAML {
		return d.Printer.YAML(v)
	}
	return d.Printer.JSON(v)
}

// rootFlags are the persistent flags every subcommand inherits. They are
// resolved against environment variables and profiles in PersistentPreRunE.
type rootFlags struct {
	configPath string
	gatewayURL string
	profile    string
	format     string
	ascii      bool
}

// NewRoot builds the command tree. Tests construct it directly so they can
// swap stdout/stderr; main() wraps it in fang for styled help and errors.
func NewRoot() *cobra.Command {
	f := &rootFlags{}
	root := &cobra.Command{
		Use:   "ferro",
		Short: "Operator console for the Ferro Labs AI Gateway",
		Long: "ferro manages a running Ferro Labs AI Gateway over its HTTP admin API.\n" +
			"With no arguments it opens a full-screen operations console; with a verb\n" +
			"it runs a scriptable command whose output belongs to your pipeline.",
		// Errors go to stderr on their own; the usage wall on every failure
		// buries the one line that says what went wrong.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// The full-screen console needs a terminal. On a pipe or a file,
			// drawing it would spray escape codes into somebody's stdout, so
			// refuse and point at the scriptable surface instead.
			if !stdinIsTTY() || !stdoutIsTTY() {
				return errors.New("ferro's full-screen console needs terminal input and output; " +
					"run `ferro --help` for the scriptable commands")
			}
			d := deps(cmd)
			// tui takes the command tree rather than importing this package:
			// root.go calls tui.Run, so the reverse import would be a cycle.
			// Passing cmd.Root() keeps the console's verb vocabulary derived
			// from the real cobra tree, so the two cannot drift. It always draws
			// on stdout, so its colour decision is stdout's TTY state -- unlike
			// the Printer's below, which follows stderr, the stream it writes to.
			return tui.Run(d.Client, d.Resolved,
				theme.Mode{Color: !NoColor(os.Stdout), ASCII: f.ascii}, cmd.Root())
		},
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			// version must work offline, with no config and no credential.
			if cmd.Name() == "version" || cmd.Name() == "help" || isCompletion(cmd) {
				return nil
			}

			// --config outranks FERRO_CONFIG, which outranks the OS default --
			// the same precedence --gateway-url takes over FERRO_URL below.
			path := config.DefaultPath()
			if v := os.Getenv(config.EnvConfigPath); v != "" {
				path = v
			}
			if f.configPath != "" {
				path = f.configPath
			}
			file, err := config.Load(path)
			if err != nil {
				return err
			}
			resolved, err := config.Resolve(file, f.gatewayURL, f.profile, os.Getenv)
			if err != nil {
				return err
			}

			format := Format(f.format)
			switch format {
			case FormatTable, FormatJSON, FormatYAML:
			default:
				return fmt.Errorf("unknown --format %q (want table, json, or yaml)", f.format)
			}

			client, err := api.New(resolved.URL, resolved.APIKey,
				api.WithUserAgent("ferro-cli/"+version.Version))
			if err != nil {
				return err
			}

			d := &runtimeDeps{
				Resolved: resolved,
				Printer: &Printer{
					Out:    cmd.OutOrStdout(),
					Err:    cmd.ErrOrStderr(),
					Format: format,
					// note() (OK/Warn/Fail) always writes to Err, so the colour
					// decision follows stderr's own TTY state, not stdout's --
					// `ferro status 2> run.log` must not leak ANSI into the file.
					Color: !NoColor(os.Stderr),
					ASCII: f.ascii,
				},
				Client: client,
			}
			cmd.SetContext(context.WithValue(cmd.Context(), depsKey{}, d))
			return nil
		},
	}

	pf := root.PersistentFlags()
	pf.StringVar(&f.configPath, "config", "", "path to the ferro config file (env "+config.EnvConfigPath+")")
	pf.StringVar(&f.gatewayURL, "gateway-url", "", "gateway base URL (env FERRO_URL)")
	pf.StringVar(&f.profile, "profile", "", "connection profile from the ferro config file")
	pf.StringVar(&f.format, "format", "table", "output format: table|json|yaml")
	pf.BoolVar(&f.ascii, "ascii", false, "ASCII-only glyphs ([OK] [X] [!] [-])")

	root.AddCommand(
		newVersionCmd(),
		newStatusCmd(),
		newKeysCmd(),
		newLogsCmd(),
		newModelsCmd(),
		newProvidersCmd(),
		newServicesCmd(),
		newMCPCmd(),
		newPluginsCmd(),
		newSessionsCmd(),
		newAuditCmd(),
		newChatCmd(),
	)
	return root
}

var stdoutIsTTY = func() bool { return isTTY(os.Stdout) }

// completionVerb is the name cobra gives the generated shell-completion
// command. The hidden helpers a shell actually runs on every Tab
// (__complete, __completeNoDesc) share cobra's own ShellCompRequestCmd as
// their prefix. None of them may load config or resolve a credential.
const completionVerb = "completion"

func isCompletion(cmd *cobra.Command) bool {
	for ; cmd != nil; cmd = cmd.Parent() {
		if cmd.Name() == completionVerb || strings.HasPrefix(cmd.Name(), cobra.ShellCompRequestCmd) {
			return true
		}
	}
	return false
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print ferro version, commit, and build date",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "ferro %s\n", version.String())
			return nil
		},
	}
}
