package cli

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// InitCmd scaffolds a config file and generates a master key.
var InitCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a new gateway configuration",
	Long: `Creates a minimal configuration file and generates a master key
for authenticating with the dashboard and admin API.`,
	RunE: runInit,
}

func init() {
	InitCmd.Flags().String("config-format", "yaml", "Config file format: yaml or json")
	InitCmd.Flags().StringP("output", "o", "", "Config file path (default: config.yaml or config.json)")
	InitCmd.Flags().Bool("non-interactive", false, "Skip prompts, use defaults")
}

// GenerateMasterKey returns a random key with fgw_ prefix.
func GenerateMasterKey() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return "fgw_" + hex.EncodeToString(b)
}

const defaultConfigYAML = `# Ferro Labs AI Gateway configuration
# Docs: https://docs.ferrolabs.ai/configuration

strategy:
  mode: fallback

targets:
  - virtual_key: openai
  - virtual_key: anthropic
  # - virtual_key: gemini
  # - virtual_key: groq
  # - virtual_key: mistral

# plugins: []
`

type defaultConfigJSON struct {
	Strategy struct {
		Mode string `json:"mode"`
	} `json:"strategy"`
	Targets []struct {
		VirtualKey string `json:"virtual_key"`
	} `json:"targets"`
}

// WriteDefaultConfig writes a minimal config file. Returns error if file exists.
func WriteDefaultConfig(path, format string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("file already exists: %s", path)
	}

	var data []byte
	switch strings.ToLower(format) {
	case FormatJSON:
		cfg := defaultConfigJSON{}
		cfg.Strategy.Mode = "fallback"
		cfg.Targets = []struct {
			VirtualKey string `json:"virtual_key"`
		}{
			{VirtualKey: "openai"},
			{VirtualKey: "anthropic"},
		}
		var err error
		data, err = json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return err
		}
		data = append(data, '\n')
	default:
		data = []byte(defaultConfigYAML)
	}

	return os.WriteFile(path, data, 0600)
}

func runInit(cmd *cobra.Command, _ []string) error {
	format, _ := cmd.Flags().GetString("config-format")
	output, _ := cmd.Flags().GetString("output")
	nonInteractive, _ := cmd.Flags().GetBool("non-interactive")

	out := os.Stderr
	// eprintf writes to stderr; errors are intentionally ignored — if stderr is
	// unavailable there is nothing useful we can do from a CLI init command.
	eprintf := func(format string, a ...any) { _, _ = fmt.Fprintf(out, format, a...) }

	if !nonInteractive {
		eprintf("\n")
		eprintf("%s\n", Clr(ColorBold+ColorWhite, "  Ferro Labs AI Gateway — Setup"))
		eprintf("\n")
	}

	// Resolve format.
	format = strings.ToLower(format)
	if format != FormatJSON {
		format = FormatYAML
	}

	// Resolve output path.
	if output == "" {
		if format == FormatJSON {
			output = "config.json"
		} else {
			output = "config.yaml"
		}
	}

	// Generate master key.
	masterKey := GenerateMasterKey()

	// Write config file.
	err := WriteDefaultConfig(output, format)
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			eprintf("  %s %s\n", Clr(ColorYellow, "!"), err)
			eprintf("    Skipping config file creation.\n")
		} else {
			return err
		}
	} else {
		eprintf("  %s Created %s\n", Clr(ColorGreen, "✓"), output)
	}

	// Print master key to stderr so it is not captured by shell redirections or CI log scrapers.
	eprintf("\n")
	eprintf("  Master key: %s\n", Clr(ColorBold+ColorOrange, masterKey))
	eprintf("\n")
	eprintf("  %s Save this key — you'll need it for the dashboard and API.\n", Clr(ColorYellow, "!"))
	eprintf("    Set it as an environment variable:\n")
	eprintf("    %s\n", Clr(ColorDim, "export MASTER_KEY="+masterKey))
	eprintf("\n")
	eprintf("%s\n", Clr(ColorBold+ColorWhite, "  Next steps:"))
	eprintf("    1. Set provider API keys (e.g. export OPENAI_API_KEY=sk-...)\n")
	eprintf("    2. Start the gateway: %s\n", Clr(ColorCyan, "ferrogw serve"))
	eprintf("    3. Open dashboard:    %s\n", Clr(ColorCyan, "http://localhost:8080/dashboard"))
	eprintf("\n")

	return nil
}
