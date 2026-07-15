package cli

import (
	"fmt"
	"runtime"

	"github.com/ferro-labs/ai-gateway/internal/version"
	"github.com/spf13/cobra"
)

// VersionCmd prints version information.
var VersionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	RunE:  runVersion,
}

func runVersion(cmd *cobra.Command, _ []string) error {
	info := map[string]string{
		"version": version.Version,
		"commit":  version.Commit,
		"built":   version.Date,
		"go":      runtime.Version(),
		"os_arch": runtime.GOOS + "/" + runtime.GOARCH,
	}

	pr := printerFromCmd(cmd)
	if pr.Format != FormatTable {
		return pr.Print(info)
	}

	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(out, "  %-12s %s\n", Clr(ColorBold, "Version"), Clr(ColorYellow, version.Version))
	_, _ = fmt.Fprintf(out, "  %-12s %s\n", Clr(ColorBold, "Commit"), version.Commit)
	_, _ = fmt.Fprintf(out, "  %-12s %s\n", Clr(ColorBold, "Built"), version.Date)
	_, _ = fmt.Fprintf(out, "  %-12s %s\n", Clr(ColorBold, "Go"), runtime.Version())
	_, _ = fmt.Fprintf(out, "  %-12s %s\n", Clr(ColorBold, "OS/Arch"), runtime.GOOS+"/"+runtime.GOARCH)
	return nil
}
