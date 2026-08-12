package command

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ferro-labs/ai-gateway/cli/internal/table"
)

func newModelsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "models",
		Short: "List the models this gateway routes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d := deps(cmd)
			models, err := d.Client.Models(cmd.Context())
			if err != nil {
				return err
			}
			if d.Printer.Format != FormatTable {
				return d.printStructured(models)
			}
			rows := make([][]string, 0, len(models))
			for _, m := range models {
				rows = append(rows, []string{
					m.ID,
					table.OrDash(m.OwnedBy),
					table.OrDash(m.Mode),
					table.PositiveOrDash(m.ContextWindow),
					fmt.Sprintf("%d", len(m.Capabilities)),
					table.OrDash(m.Status),
				})
			}
			d.Printer.Table([]string{"ID", "OWNED BY", "MODE", "CONTEXT", "CAPABILITIES", colStatus}, rows)
			return nil
		},
	}
}
