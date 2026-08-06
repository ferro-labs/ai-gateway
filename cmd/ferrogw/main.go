// Package main is the entry point for the ferrogw gateway server and CLI.
package main

import (
	"os"

	"github.com/ferro-labs/ai-gateway/internal/bootstrap"
	"github.com/ferro-labs/ai-gateway/internal/cli"
	"github.com/spf13/cobra"

	// Register built-in plugins so they can be loaded from config.
	_ "github.com/ferro-labs/ai-gateway/plugin/budget"
	_ "github.com/ferro-labs/ai-gateway/plugin/cache"
	_ "github.com/ferro-labs/ai-gateway/plugin/logger"
	_ "github.com/ferro-labs/ai-gateway/plugin/maxtoken"
	_ "github.com/ferro-labs/ai-gateway/plugin/ratelimit"
	_ "github.com/ferro-labs/ai-gateway/plugin/wordfilter"
)

// newRootCmd builds the fully wired command tree. serve is what the bare
// `ferrogw` and `ferrogw serve` run; it is a parameter so the argument contract
// below can be tested without binding a port.
func newRootCmd(serve func()) *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "ferrogw",
		Short: "Ferro Labs AI Gateway",
		Long:  "High-performance AI gateway with smart routing, plugins, and an authenticated Admin API.",
		// Args is deliberately LEFT NIL, and that is load-bearing. The root is
		// both the server entrypoint and the parent of every subcommand, so an
		// unmatched first token must not fall through and boot a gateway.
		// Cobra's Find applies legacyArgs — which rejects an unknown token for a
		// root that has subcommands, and names the command you probably meant —
		// but ONLY when Args is nil. Setting NoArgs here still rejects and loses
		// the suggestion; setting ArbitraryArgs makes `ferrogw valdate cfg.yaml`
		// start a server on :8080. TestRootRejectsUnknownSubcommand pins it.
		// When a command returns an error, the error is the answer. Cobra
		// otherwise prints the whole usage block after it — and prints it to
		// STDOUT while the error goes to stderr, so a failing `ferrogw validate`
		// wrote a flag list into whatever was reading its output. Setting this on
		// the root covers every subcommand: cobra checks the root's flag as well
		// as the failing command's.
		SilenceUsage: true,
		// Default: start the server (backward compatible).
		Run: func(_ *cobra.Command, _ []string) {
			serve()
		},
	}

	serveCmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the gateway server",
		Args:  cobra.NoArgs,
		Run: func(_ *cobra.Command, _ []string) {
			serve()
		},
	}

	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(cli.InitCmd)
	rootCmd.AddCommand(cli.ValidateCmd)
	rootCmd.AddCommand(cli.PluginsCmd)
	rootCmd.AddCommand(cli.DoctorCmd)
	rootCmd.AddCommand(cli.StatusCmd)
	rootCmd.AddCommand(cli.VersionCmd)
	rootCmd.AddCommand(cli.AdminCmd)

	// Persistent flags for CLI commands.
	rootCmd.PersistentFlags().String("gateway-url", "",
		"Gateway base URL (env: FERROGW_URL, default: http://localhost:8080)")
	rootCmd.PersistentFlags().String("api-key", "",
		"Admin API key (env: FERROGW_API_KEY)")
	rootCmd.PersistentFlags().String(cli.FlagFormat, cli.FormatTable,
		"Output format: table, json, or yaml (not supported by the report commands: init, doctor, status)")

	return rootCmd
}

func main() {
	if err := newRootCmd(bootstrap.Serve).Execute(); err != nil {
		os.Exit(1)
	}
}
