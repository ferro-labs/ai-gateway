package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// StatusCmd checks the health of a running gateway.
var StatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check health of a running gateway instance",
	Args:  cobra.NoArgs,
	RunE:  runStatus,
}

func runStatus(cmd *cobra.Command, _ []string) error {
	if err := requireDefaultFormat(cmd); err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	c := adminClientFromCmd(cmd)

	start := time.Now()
	var health map[string]any
	if err := c.GetHealth(cmd.Context(), "/health", &health); err != nil {
		// An unreachable gateway is a failure, not a state to report: `ferrogw
		// status || alert` has to be able to see it, and it could not while this
		// exited 0. A gateway that ANSWERS stays exit-0 however unhappy the
		// answer — 503-degraded is reported below and is not an error here.
		//
		// The diagnostic is returned rather than printed: cobra writes a
		// returned error to stderr, which keeps stdout the machine channel and
		// leaves `ferrogw status | jq` reading only what status chose to emit.
		return fmt.Errorf("gateway unreachable at %s: %w", c.BaseURL, err)
	}
	latency := time.Since(start)

	// /health answers 503 while degraded (e.g. no providers configured). The
	// gateway is up; say what is actually wrong instead of "unreachable".
	// A body with no usable status is reported as such rather than as healthy.
	status, symbol, color := "unknown", SymWARN, ColorYellow
	if s, ok := health["status"].(string); ok {
		if s == "ok" {
			status, symbol, color = "healthy", SymOK, ColorGreen
		} else {
			status = s
		}
	}
	_, _ = fmt.Fprintf(out, "  %s %s -- %s (%s)\n",
		Clr(color, symbol),
		c.BaseURL,
		Clr(ColorBold+color, status),
		latency.Round(time.Millisecond),
	)

	if v, ok := health["version"]; ok {
		_, _ = fmt.Fprintf(out, "  Version: %s\n", Clr(ColorYellow, fmt.Sprint(v)))
	}

	// Try to get provider count.
	var provResp []map[string]any
	if err := c.Get(cmd.Context(), "/admin/providers", &provResp); err == nil && len(provResp) > 0 {
		models := 0
		for _, p := range provResp {
			if m, ok := p["models"].([]any); ok {
				models += len(m)
			}
		}
		_, _ = fmt.Fprintf(out, "  Providers: %s (%d models)\n",
			Clr(ColorCyan, fmt.Sprintf("%d", len(provResp))), models)
	}

	return nil
}
