// Package main provides the ferrogw-cli command-line tool for managing the
// Ferro Labs AI Gateway. It replaces the former hand-rolled arg parser with
// Cobra so that new command groups, persistent flags, and shell completions are
// first-class citizens.
package main

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/version"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/spf13/cobra"

	// Register built-in plugins so they appear in the plugin list.
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/budget"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/cache"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/logger"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/maxtoken"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/pii"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/promptshield"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/regexguard"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/schemaguard"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/secretscan"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/wordfilter"
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// banner returns the ASCII art header printed on bare invocation.
func banner() string {
	const (
		cyan   = "\033[96m"
		bold   = "\033[1m"
		dim    = "\033[2m"
		yellow = "\033[93m"
		white  = "\033[97m"
		reset  = "\033[0m"
	)

	// Compact "F" faithful to the Ferro Labs negative-space block design:
	//  - full-width top/middle/bottom bands
	//  - upper/lower taper sections (gap right)
	//  - arm sections (gap left, arm fills right)
	art := []string{
		"██████████████████████",
		"██████████████████████",
		"██████████   █████████",
		"██████   █████████████",
		"██████   █████████████",
		"██████████████████████",
		"█████████     ████████",
		"██████   █████████████",
		"██████   █████████████",
		"██████████████████████",
		"██████████████████████",
	}

	text := []string{
		bold + white + "FERRO LABS  ·  AI GATEWAY" + reset,
		dim + "─────────────────────────────────────" + reset,
		"Version · " + yellow + version.Short() + reset,
		"",
		"AI Infrastructure Management CLI",
		"",
		"Validate configs · Inspect plugins",
		"Manage gateway via Admin API",
	}

	leftWidth := 0
	for _, line := range art {
		if w := utf8.RuneCountInString(line); w > leftWidth {
			leftWidth = w
		}
	}

	maxLines := len(art)
	if len(text) > maxLines {
		maxLines = len(text)
	}
	textStart := 0
	if len(art) > len(text) {
		textStart = (len(art) - len(text)) / 2
	}

	var b strings.Builder
	for i := 0; i < maxLines; i++ {
		leftRunes := 0
		if i < len(art) {
			left := art[i]
			leftRunes = utf8.RuneCountInString(left)
			b.WriteString(cyan)
			b.WriteString(left)
			b.WriteString(reset)
		}
		b.WriteString(strings.Repeat(" ", leftWidth-leftRunes))
		b.WriteString("  ")
		b.WriteString(dim + "│" + reset)
		b.WriteString("  ")
		textIndex := i - textStart
		if textIndex >= 0 && textIndex < len(text) {
			b.WriteString(text[textIndex])
		}
		b.WriteByte('\n')
	}
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

func init() {
	// Persistent flags available on every sub-command.
	rootCmd.PersistentFlags().String("gateway-url", "",
		"Gateway base URL (env: FERROGW_URL, default: http://localhost:8080)")
	rootCmd.PersistentFlags().String("api-key", "",
		"Admin API key (env: FERROGW_API_KEY)")
	rootCmd.PersistentFlags().String("format", "table",
		"Output format: table, json, or yaml")

	// Top-level commands.
	rootCmd.AddCommand(validateCmd)
	rootCmd.AddCommand(pluginsCmd)
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(adminCmd) // defined in admin.go
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
		pr := newPrinter(formatFlag(cmd))
		if pr.format != formatTable {
			return pr.Print(map[string]string{"version": version.String()})
		}
		fmt.Printf("ferrogw-cli %s\n", version.String())
		return nil
	},
}
