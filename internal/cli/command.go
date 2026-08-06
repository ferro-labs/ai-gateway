package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// FlagFormat is the root persistent flag selecting the output encoding.
const FlagFormat = "format"

// adminClientFromCmd builds an AdminClient from the gateway-url and api-key
// persistent flags on the command's root.
func adminClientFromCmd(cmd *cobra.Command) *AdminClient {
	flagURL, _ := cmd.Root().PersistentFlags().GetString("gateway-url")
	flagKey, _ := cmd.Root().PersistentFlags().GetString("api-key")
	return NewAdminClient(flagURL, flagKey)
}

// formatFlag returns the requested output format, defaulting to table.
func formatFlag(cmd *cobra.Command) string {
	format, _ := cmd.Root().PersistentFlags().GetString(FlagFormat)
	if format == "" {
		return FormatTable
	}
	return strings.ToLower(format)
}

// requireDefaultFormat is the guard for the commands that render a human report
// rather than a data structure — init, doctor, status.
//
// --format is a root persistent flag because the commands that serve records
// (keys, plugins, validate, version) all honour it, and cobra advertises a
// persistent flag under every subcommand. The report commands cannot honour it:
// there is no useful JSON encoding of a health narrative, and inventing one
// would make the machine contract whatever the prose happened to say that
// release. Saying so is the honest answer — `ferrogw status --format json | jq`
// used to receive ANSI-decorated text and fail to parse it with no clue why.
func requireDefaultFormat(cmd *cobra.Command) error {
	format := formatFlag(cmd)
	if format == FormatTable {
		return nil
	}
	hint := ""
	if cmd.Name() == "init" {
		hint = " (to choose the config file's encoding, use --config-format)"
	}
	return fmt.Errorf("--format %s is not supported by %q: it reports human-readable status, not structured data%s",
		format, cmd.Name(), hint)
}

// printerFromCmd builds a Printer using the format persistent flag on the
// command's root and bound to the command's output writer, so command output
// is redirectable (via cmd.SetOut) and therefore testable.
func printerFromCmd(cmd *cobra.Command) *Printer {
	pr := NewPrinter(formatFlag(cmd))
	pr.Out = cmd.OutOrStdout()
	return pr
}

// printResult renders v using the format persistent flag on the command's root.
func printResult(cmd *cobra.Command, v any) error {
	return printerFromCmd(cmd).Print(v)
}
