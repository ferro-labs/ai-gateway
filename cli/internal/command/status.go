package command

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ferro-labs/ai-gateway/cli/internal/table"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Health of a running gateway (exit 1 only when unreachable)",
		Long: "status prints one row describing the gateway this profile points at.\n\n" +
			"The exit code is the contract: 1 means the gateway could not be reached\n" +
			"at all, so `ferro status || alert` pages on an outage and stays quiet for\n" +
			"a degraded-but-serving gateway, which exits 0 with its state printed.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d := deps(cmd)
			// Status returns a report even when it fails, naming the URL and the
			// state it reached — printing it is what makes state:"unreachable"
			// visible to a `--format json` consumer instead of an empty stdout.
			report, statusErr := d.Client.Status(cmd.Context())

			if d.Printer.Format != FormatTable {
				if err := d.printStructured(report); err != nil {
					return err
				}
			} else {
				mcp := "-"
				if report.MCP != nil {
					mcp = fmt.Sprintf("%d/%d", report.MCP.Ready, report.MCP.Total)
				}
				// A not_ready gateway reports no targets array at all. Rendering
				// "0/0" there would read as "none configured" when the truth is
				// "yours are dead" — the warnings below carry the real reason.
				targets := "-"
				if report.Targets != nil {
					targets = fmt.Sprintf("%d/%d", report.Targets.Routable, report.Targets.Total)
				}
				d.Printer.Table(
					[]string{colState, colURL, "LATENCY", "TARGETS", "PROVIDERS", colModels, "MCP", "AUTH"},
					[][]string{{
						report.State,
						report.URL,
						fmt.Sprintf("%dms", report.LatencyMs),
						targets,
						table.CountOrDash(report.Providers),
						table.CountOrDash(report.Models),
						mcp,
						table.OrDash(report.Auth),
					}},
				)
			}
			for _, w := range report.Warnings {
				d.Printer.Warn("%s", w)
			}
			// Degraded stays exit 0; only an unreachable gateway is exit 1.
			return statusErr
		},
	}
}
