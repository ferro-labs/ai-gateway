package cli

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/spf13/cobra"
)

// InitCmd scaffolds a config file and generates a master key.
var InitCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a new gateway configuration",
	Long: `Creates a minimal configuration file and generates a master key
for authenticating with the Admin API and standalone web application.`,
	Args: cobra.NoArgs,
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

// placeholderProvider is the target scaffolded when the environment holds no
// provider credentials at all. A config with no targets fails validation, so
// the file has to name one; it is labelled as a placeholder instead of being
// presented as something this machine can serve.
const placeholderProvider = "openai"

const configHeader = `# Ferro Labs AI Gateway configuration
# Docs: https://docs.ferrolabs.ai/configuration

strategy:
  mode: fallback

`

const detectedTargetsNote = `# Targets are tried in order. These were scaffolded from the provider
# credentials present in the environment -- add or remove them as your keys change.
`

const placeholderTargetsNote = `# No provider credentials were found in the environment, and a config needs at
# least one target, so the entry below is a placeholder rather than a provider
# this machine can reach. Set OPENAI_API_KEY, or swap it for a provider you do
# hold a key for, before starting the gateway.
`

// The commented block keeps the generated file readable as documentation: a
// first-time reader still sees the fields a target accepts, not only the bare
// keys this environment happened to produce. It is indented so that removing
// the "# " from a line leaves valid YAML.
const configFooter = `  # A target may also carry a weight and a retry policy:
  # - virtual_key: gemini
  #   weight: 1.0
  #   retry:
  #     attempts: 3

# plugins: []
`

type configTarget struct {
	VirtualKey string `json:"virtual_key"`
}

type defaultConfigJSON struct {
	Strategy struct {
		Mode string `json:"mode"`
	} `json:"strategy"`
	Targets []configTarget `json:"targets"`
}

// credentialedProviders returns the IDs of the built-in providers whose
// credentials are present in the environment, in registry order.
//
// The gate is ProviderConfigFromEnv -- the same one auto-registration applies at
// startup -- so a scaffolded target is one this deployment will actually serve
// rather than a guess that reports unavailable on the first request.
func credentialedProviders() []string {
	entries := providers.AllProviders()
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if providers.ProviderConfigFromEnv(entry) != nil {
			ids = append(ids, entry.ID)
		}
	}
	return ids
}

// scaffoldTargets maps the detected providers onto the targets to write,
// substituting the placeholder when nothing was detected.
func scaffoldTargets(detected []string) []string {
	if len(detected) == 0 {
		return []string{placeholderProvider}
	}
	return detected
}

func renderConfigYAML(detected []string) string {
	note := detectedTargetsNote
	if len(detected) == 0 {
		note = placeholderTargetsNote
	}

	var b strings.Builder
	b.WriteString(configHeader)
	b.WriteString(note)
	b.WriteString("targets:\n")
	for _, id := range scaffoldTargets(detected) {
		b.WriteString("  - virtual_key: " + id + "\n")
	}
	b.WriteString(configFooter)
	return b.String()
}

// renderConfigJSON produces the same targets as the YAML form. JSON carries no
// comments, so the placeholder caveat reaches the operator on stderr only.
func renderConfigJSON(detected []string) ([]byte, error) {
	targets := scaffoldTargets(detected)

	cfg := defaultConfigJSON{}
	cfg.Strategy.Mode = "fallback"
	cfg.Targets = make([]configTarget, 0, len(targets))
	for _, id := range targets {
		cfg.Targets = append(cfg.Targets, configTarget{VirtualKey: id})
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// WriteDefaultConfig writes a minimal config file naming the providers this
// environment holds credentials for. Returns error if file exists.
func WriteDefaultConfig(path, format string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("file already exists: %s", path)
	}

	detected := credentialedProviders()

	var data []byte
	switch strings.ToLower(format) {
	case FormatJSON:
		var err error
		data, err = renderConfigJSON(detected)
		if err != nil {
			return err
		}
	default:
		data = []byte(renderConfigYAML(detected))
	}

	return os.WriteFile(path, data, 0600)
}

func runInit(cmd *cobra.Command, _ []string) error {
	if err := requireDefaultFormat(cmd); err != nil {
		return err
	}
	format, _ := cmd.Flags().GetString("config-format")
	output, _ := cmd.Flags().GetString("output")
	nonInteractive, _ := cmd.Flags().GetBool("non-interactive")

	out := cmd.ErrOrStderr()
	eprintf := func(format string, a ...any) { _, _ = fmt.Fprintf(out, format, a...) }

	if !nonInteractive {
		eprintf("\n  Ferro Labs AI Gateway -- Setup\n\n")
	}

	format = strings.ToLower(format)
	if format != FormatJSON {
		format = FormatYAML
	}

	if output == "" {
		if format == FormatJSON {
			output = "config.json"
		} else {
			output = "config.yaml"
		}
	}

	switch err := WriteDefaultConfig(output, format); {
	case err == nil:
		eprintf("  %s Created %s\n", Clr(ColorGreen, SymOK), output)
		// Which targets landed in the file is the operator's first surprise if
		// it disagrees with their keys, so state it rather than let them find
		// out from an unavailable provider at serve time.
		detected := credentialedProviders()
		if len(detected) == 0 {
			eprintf("  %s No provider credentials detected -- %s names %s as a placeholder target.\n",
				Clr(ColorYellow, SymWARN), output, placeholderProvider)
		} else {
			eprintf("  %s Targets from detected credentials: %s\n",
				Clr(ColorGreen, SymOK), strings.Join(detected, ", "))
		}

		// The key is generated here, not before the write: init persists it
		// nowhere, so a key printed on a run that created nothing is a
		// credential that looks live and authenticates against nothing. A
		// re-run used to print a fresh one under the same "(skipped)" notice
		// that said the config was left alone.
		masterKey := GenerateMasterKey()
		eprintf("  %s Master key: %s\n", Clr(ColorGreen, SymOK), Clr(ColorBold+ColorOrange, masterKey))
		eprintf("\n")
		eprintf("  %s Save this key -- you need it for the Admin API and web application.\n", Clr(ColorYellow, SymWARN))
		eprintf("    export MASTER_KEY=%s\n", masterKey)
	case strings.Contains(err.Error(), "already exists"):
		eprintf("  %s %s (skipped)\n", Clr(ColorYellow, SymWARN), err)
		eprintf("  %s No master key generated -- %s was left as it is, so the key this deployment already uses still applies.\n",
			Clr(ColorDim, SymDASH), output)
	default:
		return err
	}

	eprintf("\n")
	eprintf("  Next steps:\n")
	// The server reads a config file only when GATEWAY_CONFIG names one -- without
	// the export it falls back to the auto-derived defaults and ignores this file.
	eprintf("    1. Use this config:    export GATEWAY_CONFIG=%s\n", output)
	eprintf("    2. Set provider API keys (e.g. export OPENAI_API_KEY=sk-...)\n")
	eprintf("    3. Start the gateway:  ferrogw serve\n")
	eprintf("    4. Check readiness:    curl http://localhost:8080/readyz\n")
	eprintf("\n")

	return nil
}
