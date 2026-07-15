package cli

import "github.com/spf13/cobra"

// adminClientFromCmd builds an AdminClient from the gateway-url and api-key
// persistent flags on the command's root.
func adminClientFromCmd(cmd *cobra.Command) *AdminClient {
	flagURL, _ := cmd.Root().PersistentFlags().GetString("gateway-url")
	flagKey, _ := cmd.Root().PersistentFlags().GetString("api-key")
	return NewAdminClient(flagURL, flagKey)
}

// printerFromCmd builds a Printer using the format persistent flag on the
// command's root and bound to the command's output writer, so command output
// is redirectable (via cmd.SetOut) and therefore testable.
func printerFromCmd(cmd *cobra.Command) *Printer {
	format, _ := cmd.Root().PersistentFlags().GetString("format")
	pr := NewPrinter(format)
	pr.Out = cmd.OutOrStdout()
	return pr
}

// printResult renders v using the format persistent flag on the command's root.
func printResult(cmd *cobra.Command, v any) error {
	return printerFromCmd(cmd).Print(v)
}
