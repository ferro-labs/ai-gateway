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
	fmt.Println("  Provider API Keys")

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
			fmt.Printf("    %s %s\n", Clr(ColorGreen, SymOK), p.name)
			found++
		} else {
			fmt.Printf("    %s %s\n", Clr(ColorDim, SymDASH), p.name)
		}
	}

	if found == 0 {
		fmt.Printf("\n    %s no provider API keys detected\n", Clr(ColorYellow, SymWARN))
	} else {
		fmt.Printf("\n    %d found\n", found)
	}

	// Configuration check.
	fmt.Println()
	fmt.Println("  Configuration")
	cfgPath := os.Getenv("GATEWAY_CONFIG")
	if cfgPath == "" {
		fmt.Printf("    %s GATEWAY_CONFIG not set (using defaults)\n", Clr(ColorDim, SymDASH))
	} else {
		cfg, err := aigateway.LoadConfig(cfgPath)
		if err != nil {
			fmt.Printf("    %s %s: %v\n", Clr(ColorRed, SymFAIL), cfgPath, err)
		} else if err := aigateway.ValidateConfig(*cfg); err != nil {
			fmt.Printf("    %s %s: %v\n", Clr(ColorRed, SymFAIL), cfgPath, err)
		} else {
			fmt.Printf("    %s %s (strategy=%s, targets=%d)\n",
				Clr(ColorGreen, SymOK), cfgPath, cfg.Strategy.Mode, len(cfg.Targets))
		}
	}

	// Master key check.
	fmt.Println()
	fmt.Println("  Auth")
	if os.Getenv("MASTER_KEY") != "" {
		fmt.Printf("    %s MASTER_KEY is set\n", Clr(ColorGreen, SymOK))
	} else {
		fmt.Printf("    %s MASTER_KEY not set -- run 'ferrogw init' to generate one\n", Clr(ColorYellow, SymWARN))
	}

	// Connectivity check.
	fmt.Println()
	fmt.Println("  Gateway Connectivity")

	flagURL, _ := cmd.Root().PersistentFlags().GetString("gateway-url")
	flagKey, _ := cmd.Root().PersistentFlags().GetString("api-key")
	c := NewAdminClient(flagURL, flagKey)
	var h struct {
		Status string `json:"status"`
	}
	start := time.Now()
	err := c.Get("/health", &h)
	latency := time.Since(start)
	if err != nil {
		fmt.Printf("    %s %s: %v\n", Clr(ColorRed, SymFAIL), c.BaseURL, err)
	} else {
		fmt.Printf("    %s %s -- healthy (%dms)\n", Clr(ColorGreen, SymOK), c.BaseURL, latency.Milliseconds())
	}

	fmt.Println()
	return nil
}
