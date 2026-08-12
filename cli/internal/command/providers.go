package command

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ferro-labs/ai-gateway/cli/internal/api"
	"github.com/ferro-labs/ai-gateway/cli/internal/table"
)

// providerRow is the merged view: no single endpoint serves all five columns.
// /health is unauthenticated and carries the circuit; /admin/health needs a
// credential and carries the status, the model count and the failure message.
type providerRow struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Circuit string `json:"circuit"`
	Models  int    `json:"models"`
	Message string `json:"message,omitempty"`
}

func mergeProviders(h *api.HealthReport, ah *api.AdminHealth) []providerRow {
	circuits := map[string]string{}
	for _, p := range h.Providers {
		circuits[p.Name] = p.Circuit
	}
	if ah == nil {
		// Unauthenticated is a smaller answer, not an error.
		rows := make([]providerRow, 0, len(h.Providers))
		for _, p := range h.Providers {
			rows = append(rows, providerRow{
				Name: p.Name, Status: p.Status, Circuit: p.Circuit,
				Models: p.Models,
			})
		}
		return rows
	}
	rows := make([]providerRow, 0, max(len(ah.Providers), len(h.Providers)))
	seen := make(map[string]struct{}, len(ah.Providers))
	for _, p := range ah.Providers {
		seen[p.Name] = struct{}{}
		rows = append(rows, providerRow{
			Name: p.Name, Status: p.Status, Circuit: circuits[p.Name],
			Models: p.Models, Message: p.Message,
		})
	}
	for _, p := range h.Providers {
		if _, ok := seen[p.Name]; ok {
			continue
		}
		rows = append(rows, providerRow{
			Name: p.Name, Status: p.Status, Circuit: p.Circuit, Models: p.Models,
		})
	}
	return rows
}

func newProvidersCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "providers",
		Short: "Provider health, circuit state and model counts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d := deps(cmd)
			health, _, err := d.Client.Health(cmd.Context())
			if err != nil {
				return err
			}
			// Best effort: an unusable credential costs the status and message
			// columns, not the command.
			admin, adminErr := d.Client.AdminHealth(cmd.Context())
			if adminErr != nil {
				admin = nil
				// Only a refused credential is reported as one; a timeout or a
				// gateway error says so instead, so the reader is not sent to
				// check a credential that was never the problem.
				why := "admin health unavailable"
				if api.IsUnauthorized(adminErr) {
					why = "no admin credential accepted"
				}
				d.Printer.Warn("%s — status and message come from /health only (%v)", why, adminErr)
			}

			rows := mergeProviders(health, admin)
			if d.Printer.Format != FormatTable {
				return d.printStructured(rows)
			}
			cells := make([][]string, 0, len(rows))
			for _, r := range rows {
				// Every cell that can be absent renders "-": an empty one would
				// collapse the column for anything splitting this table on
				// whitespace, and read as a status rather than as its absence.
				cells = append(cells, []string{
					r.Name, table.OrDash(r.Status), table.OrDash(r.Circuit),
					fmt.Sprintf("%d", r.Models), table.OrDash(r.Message),
				})
			}
			d.Printer.Table([]string{colProvider, colStatus, "CIRCUIT", colModels, "MESSAGE"}, cells)
			return nil
		},
	}
}
