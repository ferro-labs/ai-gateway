package cli

import (
	"fmt"
	"os"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/spf13/cobra"
)

// DoctorCmd runs offline environment and connectivity checks.
var DoctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check environment, configuration, and gateway connectivity",
	RunE:  runDoctor,
}

func runDoctor(cmd *cobra.Command, _ []string) error {
	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintln(out, "  Provider API Keys")

	topProviders := []struct {
		name   string
		envKey string
	}{
		{"openai", "OPENAI_API_KEY"},
		{"anthropic", "ANTHROPIC_API_KEY"},
		{"gemini", "GEMINI_API_KEY"},
		{"groq", "GROQ_API_KEY"},
		{"mistral", "MISTRAL_API_KEY"},
	}

	found := 0
	for _, p := range topProviders {
		if os.Getenv(p.envKey) != "" {
			_, _ = fmt.Fprintf(out, "    %s %s\n", Clr(ColorGreen, SymOK), p.name)
			found++
		} else {
			_, _ = fmt.Fprintf(out, "    %s %s\n", Clr(ColorDim, SymDASH), p.name)
		}
	}

	if found == 0 {
		_, _ = fmt.Fprintf(out, "\n    %s no provider API keys detected\n", Clr(ColorYellow, SymWARN))
	} else {
		_, _ = fmt.Fprintf(out, "\n    %d found\n", found)
	}

	// Configuration check.
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintln(out, "  Configuration")
	cfgPath := os.Getenv("GATEWAY_CONFIG")
	if cfgPath == "" {
		_, _ = fmt.Fprintf(out, "    %s GATEWAY_CONFIG not set (using defaults)\n", Clr(ColorDim, SymDASH))
	} else {
		cfg, err := aigateway.LoadConfig(cfgPath)
		if err != nil {
			_, _ = fmt.Fprintf(out, "    %s %s: %v\n", Clr(ColorRed, SymFAIL), cfgPath, err)
		} else if err := aigateway.ValidateConfig(*cfg); err != nil {
			_, _ = fmt.Fprintf(out, "    %s %s: %v\n", Clr(ColorRed, SymFAIL), cfgPath, err)
		} else {
			_, _ = fmt.Fprintf(out, "    %s %s (strategy=%s, targets=%d)\n",
				Clr(ColorGreen, SymOK), cfgPath, cfg.Strategy.Mode, len(cfg.Targets))
		}
	}

	// Master key check.
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintln(out, "  Auth")
	if os.Getenv("MASTER_KEY") != "" {
		_, _ = fmt.Fprintf(out, "    %s MASTER_KEY is set\n", Clr(ColorGreen, SymOK))
	} else {
		_, _ = fmt.Fprintf(out, "    %s MASTER_KEY not set -- run 'ferrogw init' to generate one\n", Clr(ColorYellow, SymWARN))
	}

	// Connectivity check.
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintln(out, "  Gateway Connectivity")

	c := adminClientFromCmd(cmd)
	var h struct {
		Status string `json:"status"`
	}
	start := time.Now()
	// GetHealth, not Get: /health answers 503 while degraded, and a degraded
	// gateway is exactly what doctor exists to diagnose.
	err := c.GetHealth(cmd.Context(), "/health", &h)
	latency := time.Since(start)
	switch {
	case err != nil:
		_, _ = fmt.Fprintf(out, "    %s %s: %v\n", Clr(ColorRed, SymFAIL), c.BaseURL, err)
	case h.Status != "ok":
		_, _ = fmt.Fprintf(out, "    %s %s -- %s (%dms)\n", Clr(ColorYellow, SymWARN), c.BaseURL, h.Status, latency.Milliseconds())
	default:
		_, _ = fmt.Fprintf(out, "    %s %s -- healthy (%dms)\n", Clr(ColorGreen, SymOK), c.BaseURL, latency.Milliseconds())
	}

	_, _ = fmt.Fprintln(out)
	return nil
}
