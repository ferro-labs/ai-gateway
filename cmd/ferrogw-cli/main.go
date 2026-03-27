// Package main provides the ferrogw-cli command-line tool for managing the
// Ferro Labs AI Gateway. It replaces the former hand-rolled arg parser with
// Cobra so that new command groups, persistent flags, and shell completions are
// first-class citizens.
package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/version"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/spf13/cobra"

	// Register built-in plugins so they appear in the plugin list.
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/budget"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/cache"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/logger"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/maxtoken"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/wordfilter"
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// banner returns the ASCII art header printed on bare invocation.
//
// Figlet "doom" font — FERRO in bold orange, LABS in dim white, side by side:
//
//	______ _________________ _____    _       ___  ______  _____
//	|  ___|  ___| ___ \ ___ \  _  |  | |     / _ \ | ___ \/  ___|
//	| |_  | |__ | |_/ / |_/ / | | |  | |    / /_\ \| |_/ /\ `--.
//	|  _| |  __||    /|    /| | | |  | |    |  _  || ___ \ `--.
//	| |   | |___| |\ \| |\ \\ \_/ /  | |____| | | || |_/ //\__/ /
//	\_|   \____/\_| \_\_| \_|\___/   \_____/\_| |_/\____/ \____/
//
//	  AI Gateway
//	  ─────────────────────────────────────────────  v1.0.0
func banner() string {
	// FERRO — figlet "doom", bold orange
	ferro := [6]string{
		"______ _________________ _____ ",
		"|  ___|  ___| ___ \\ ___ \\  _  |",
		"| |_  | |__ | |_/ / |_/ / | | |",
		"|  _| |  __||    /|    /| | | |",
		"| |   | |___| |\\ \\| |\\ \\\\ \\_/ /",
		"\\_|   \\____/\\_| \\_\\_| \\_|\\___/ ",
	}
	// LABS — figlet "doom", dim white (visually lighter)
	labs := [6]string{
		" _       ___  ______  _____ ",
		"| |     / _ \\ | ___ \\/  ___|",
		"| |    / /_\\ \\| |_/ /\\ `--. ",
		"| |    |  _  || ___ \\ `--. \\",
		"| |____| | | || |_/ //\\__/ /",
		"\\_____/\\_| |_/\\____/ \\____/ ",
	}

	// Single centered line: dot · ＡＩ ＧＡＴＥＷＡＹ  v1.0.0
	// Visual width = 30 cols, centered under FERRO LABS block (63 cols): pad = 16.
	const subPad = "                " // 16 spaces

	var b strings.Builder
	b.WriteByte('\n')
	for i := range ferro {
		fmt.Fprintf(&b, "  %s  %s\n",
			clr(colorBold+colorOrange, ferro[i]),
			clr(colorDim+colorWhite, labs[i]))
	}
	fmt.Fprintf(&b, "\n  %sＡＩ ＧＡＴＥＷＡＹ  %s  %s\n",
		subPad,
		clr(colorOrange, "·"),
		clr(colorBold+colorOrange, version.Short()))
	b.WriteByte('\n')
	return b.String()
}

// rootCmd is the top-level cobra command.
var rootCmd = &cobra.Command{
	Use:   "ferrogw-cli",
	Short: "Ferro Labs AI Gateway command-line tool",
	Long: `ferrogw-cli lets you validate configurations, inspect plugins, and manage
a running gateway instance via its Admin API.`,
	// Silence default usage printing on errors — we print the error ourselves.
	SilenceUsage: true,
	// Print the banner when invoked with no sub-command.
	Run: func(cmd *cobra.Command, _ []string) {
		fmt.Print(banner())
		_ = cmd.Help()
	},
}

// customHelpTemplate groups commands for a cleaner overview.
const customHelpTemplate = `{{.Long}}

{{if .HasAvailableSubCommands}}` + "\033[1m" + `Commands:` + "\033[0m" + `{{range .Commands}}{{if (and .IsAvailableCommand (not (eq .Name "admin")))}}
  {{rpad .Name .NamePadding}}  {{.Short}}{{end}}{{end}}

` + "\033[1m" + `Admin API:` + "\033[0m" + `{{range .Commands}}{{if (eq .Name "admin")}}{{range .Commands}}
  admin {{rpad .Name 12}}  {{.Short}}{{end}}{{end}}{{end}}
{{end}}
` + "\033[1m" + `Flags:` + "\033[0m" + `
{{.Flags.FlagUsages}}
  Learn more: https://docs.ferrolabs.ai
`

func init() {
	// Persistent flags available on every sub-command.
	rootCmd.PersistentFlags().String("gateway-url", "",
		"Gateway base URL (env: FERROGW_URL, default: http://localhost:8080)")
	rootCmd.PersistentFlags().String("api-key", "",
		"Admin API key (env: FERROGW_API_KEY)")
	rootCmd.PersistentFlags().String("format", "table",
		"Output format: table, json, or yaml")
	rootCmd.PersistentFlags().Bool("no-color", false,
		"Disable colored output (env: NO_COLOR)")

	// Wire --no-color flag into the noColor detection.
	origNoColor := noColor
	rootCmd.PersistentPreRun = func(cmd *cobra.Command, _ []string) {
		flagVal, _ := cmd.Root().PersistentFlags().GetBool("no-color")
		if flagVal {
			noColor = func() bool { return true }
		} else {
			noColor = origNoColor
		}
	}

	rootCmd.SetHelpTemplate(customHelpTemplate)

	// Top-level commands.
	rootCmd.AddCommand(validateCmd)
	rootCmd.AddCommand(pluginsCmd)
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(statusCmd) // defined in status.go
	rootCmd.AddCommand(doctorCmd) // defined in doctor.go
	rootCmd.AddCommand(adminCmd)  // defined in admin.go
}

// ── validate ──────────────────────────────────────────────────────────────────

var validateCmd = &cobra.Command{
	Use:   "validate <config-file>",
	Short: "Validate a gateway configuration file (JSON or YAML)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := args[0]

		cfg, err := aigateway.LoadConfig(path)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		if err := aigateway.ValidateConfig(*cfg); err != nil {
			return fmt.Errorf("validation failed: %w", err)
		}

		pr := newPrinter(formatFlag(cmd))

		// For json/yaml emit the parsed config; for table print a human summary.
		if pr.format != formatTable {
			return pr.Print(cfg)
		}

		fmt.Println("✓ Config is valid")
		fmt.Printf("  Strategy:  %s\n", cfg.Strategy.Mode)
		fmt.Printf("  Targets:   %d\n", len(cfg.Targets))

		var targetNames []string
		for _, t := range cfg.Targets {
			targetNames = append(targetNames, t.VirtualKey)
		}
		if len(targetNames) > 0 {
			fmt.Printf("  Providers: %s\n", strings.Join(targetNames, ", "))
		}

		if len(cfg.Plugins) > 0 {
			var pluginNames []string
			for _, p := range cfg.Plugins {
				status := "disabled"
				if p.Enabled {
					status = "enabled"
				}
				pluginNames = append(pluginNames, fmt.Sprintf("%s (%s)", p.Name, status))
			}
			fmt.Printf("  Plugins:   %s\n", strings.Join(pluginNames, ", "))
		}

		if len(cfg.Aliases) > 0 {
			fmt.Printf("  Aliases:   %d\n", len(cfg.Aliases))
		}
		return nil
	},
}

// ── plugins ───────────────────────────────────────────────────────────────────

var pluginsCmd = &cobra.Command{
	Use:   "plugins",
	Short: "List all registered built-in plugins",
	RunE: func(cmd *cobra.Command, _ []string) error {
		names := plugin.RegisteredPlugins()
		if len(names) == 0 {
			fmt.Println("No plugins registered.")
			return nil
		}

		type row struct {
			Name string `json:"name" yaml:"name"`
			Type string `json:"type" yaml:"type"`
		}
		rows := make([]row, 0, len(names))
		for _, name := range names {
			factory, _ := plugin.GetFactory(name)
			p := factory()
			rows = append(rows, row{Name: name, Type: string(p.Type())})
		}

		pr := newPrinter(formatFlag(cmd))
		if pr.format != formatTable {
			return pr.Print(rows)
		}

		fmt.Printf("%-24s %s\n", "NAME", "TYPE")
		fmt.Printf("%-24s %s\n", "----", "----")
		for _, r := range rows {
			fmt.Printf("%-24s %s\n", r.Name, r.Type)
		}
		return nil
	},
}

// ── version ───────────────────────────────────────────────────────────────────

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	RunE: func(cmd *cobra.Command, _ []string) error {
		info := map[string]string{
			"version": version.Version,
			"commit":  version.Commit,
			"built":   version.Date,
			"go":      runtime.Version(),
			"os_arch": runtime.GOOS + "/" + runtime.GOARCH,
		}

		pr := newPrinter(formatFlag(cmd))
		if pr.format != formatTable {
			return pr.Print(info)
		}

		fmt.Printf("  %-12s %s\n", clr(colorBold, "Version"), clr(colorYellow, version.Version))
		fmt.Printf("  %-12s %s\n", clr(colorBold, "Commit"), version.Commit)
		fmt.Printf("  %-12s %s\n", clr(colorBold, "Built"), version.Date)
		fmt.Printf("  %-12s %s\n", clr(colorBold, "Go"), runtime.Version())
		fmt.Printf("  %-12s %s\n", clr(colorBold, "OS/Arch"), runtime.GOOS+"/"+runtime.GOARCH)
		return nil
	},
}
