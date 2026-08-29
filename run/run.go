// Package run is the importable ferrogw program: the process lane, where the
// gateway owns the process, the command tree, and configuration. A custom
// binary is a main that blank-imports its plugins and calls Main. For the
// library lane — mounting gateway surfaces inside your own server behind
// your own middleware — see package httpgateway instead.
package run

import (
	"context"
	"os"

	"github.com/ferro-labs/ai-gateway/internal/bootstrap"
	"github.com/ferro-labs/ai-gateway/internal/cli"
	"github.com/spf13/cobra"

	// Register the built-in plugins so every binary composed on this package
	// can load them from config without repeating the list.
	_ "github.com/ferro-labs/ai-gateway/plugin/budget"
	_ "github.com/ferro-labs/ai-gateway/plugin/cache"
	_ "github.com/ferro-labs/ai-gateway/plugin/logger"
	_ "github.com/ferro-labs/ai-gateway/plugin/maxtoken"
	_ "github.com/ferro-labs/ai-gateway/plugin/ratelimit"
	_ "github.com/ferro-labs/ai-gateway/plugin/wordfilter"
)

type options struct{}

// Option configures the process runtime.
type Option func(*options)

// Run starts ferrogw and blocks until shutdown.
func Run(ctx context.Context, opts ...Option) error {
	settings := &options{}
	for _, option := range opts {
		if option != nil {
			option(settings)
		}
	}
	return bootstrap.Serve(ctx)
}

// Main runs the ferrogw CLI and owns process exit behavior.
func Main() {
	if err := newRootCmd(func() error { return Run(context.Background()) }).Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd(serve func() error) *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "ferrogw",
		Short: "Ferro Labs AI Gateway",
		Long:  "Routes AI API requests to configured providers with plugin middleware and an authenticated Admin API.",
		// Args must remain nil so Cobra reports unknown subcommands.
		// SilenceUsage avoids printing usage after errors.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runServe(cmd, serve)
		},
	}

	serveCmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the gateway server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runServe(cmd, serve)
		},
	}

	rootCmd.AddCommand(serveCmd, cli.InitCmd, cli.ValidateCmd, cli.PluginsCmd, cli.DoctorCmd,
		cli.StatusCmd, cli.VersionCmd, cli.AdminCmd)
	rootCmd.PersistentFlags().String("gateway-url", "", "Gateway base URL (env: FERROGW_URL, default: http://localhost:8080)")
	rootCmd.PersistentFlags().String("api-key", "", "Admin API key (env: FERROGW_API_KEY)")
	rootCmd.PersistentFlags().String(cli.FlagFormat, cli.FormatTable,
		"Output format: table, json, or yaml (not supported by the report commands: init, doctor, status)")
	return rootCmd
}

func runServe(cmd *cobra.Command, serve func() error) error {
	err := serve()
	if err != nil {
		// Startup already reported the failure through the structured logger.
		// Keep Cobra from printing the same error a second time.
		cmd.Root().SilenceErrors = true
	}
	return err
}
