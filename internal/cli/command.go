package cli

import "github.com/spf13/cobra"

// adminClientFromCmd builds an AdminClient from the gateway-url and api-key
// persistent flags on the command's root.
func adminClientFromCmd(cmd *cobra.Command) *AdminClient {
	flagURL, _ := cmd.Root().PersistentFlags().GetString("gateway-url")
	flagKey, _ := cmd.Root().PersistentFlags().GetString("api-key")
	return NewAdminClient(flagURL, flagKey)
}

// printResult renders v using the format persistent flag on the command's root.
func printResult(cmd *cobra.Command, v any) error {
	format, _ := cmd.Root().PersistentFlags().GetString("format")
	return NewPrinter(format).Print(v)
}
