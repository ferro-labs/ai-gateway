package command

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ferro-labs/ai-gateway/cli/internal/api"
	"github.com/ferro-labs/ai-gateway/cli/internal/table"
)

// auditOutcomes is the gateway's whole vocabulary for ?outcome=. Anything else
// is a 400 there, so it is refused here before a request is spent.
var auditOutcomes = []string{"ok", "denied", "error"}

// The four services `ferro services` summarises, one row each. Each is named
// once so the row a reachable service produces and the row its absence produces
// cannot end up calling the same service two different things.
const (
	svcMCP      = "MCP servers"
	svcPlugins  = "Plugins"
	svcSessions = "Sessions"
	svcAudit    = "Audit"
)

// stateUnsupported is the state a service row carries when this gateway does
// not serve that feature at all — a 501 rather than a count of zero, which
// would claim the feature is present and empty.
const stateUnsupported = "unsupported"

type serviceSummary struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

func newServicesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "services",
		Short: "MCP, plugin, session, and audit service summary",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d := deps(cmd)
			rows := make([]serviceSummary, 0, 4)

			servers, err := mcpServers(cmd)
			switch {
			case api.IsNotSupported(err):
				rows = append(rows, serviceSummary{Name: svcMCP, State: stateUnsupported})
			case err != nil:
				return err
			default:
				ready := 0
				for _, s := range servers {
					if s.Ready {
						ready++
					}
				}
				rows = append(rows, serviceSummary{
					Name:  svcMCP,
					State: fmt.Sprintf("%d/%d ready", ready, len(servers)),
				})
			}

			plugins, err := d.Client.Plugins(cmd.Context())
			switch {
			case api.IsNotSupported(err):
				rows = append(rows, serviceSummary{Name: svcPlugins, State: stateUnsupported})
			case err != nil:
				return err
			default:
				active := 0
				for _, p := range plugins {
					if p.Enabled {
						active++
					}
				}
				rows = append(rows, serviceSummary{Name: svcPlugins, State: fmt.Sprintf("%d active", active)})
			}

			sessions, err := d.Client.Sessions(cmd.Context())
			switch {
			case api.IsNotSupported(err):
				rows = append(rows, serviceSummary{Name: svcSessions, State: stateUnsupported})
			case err != nil:
				return err
			default:
				rows = append(rows, serviceSummary{Name: svcSessions, State: fmt.Sprintf("%d operators", len(sessions))})
			}

			audit, err := d.Client.Audit(cmd.Context(), api.AuditQuery{Limit: 1})
			switch {
			case api.IsNotSupported(err):
				rows = append(rows, serviceSummary{Name: svcAudit, State: stateUnsupported})
			case err != nil:
				return err
			default:
				rows = append(rows, serviceSummary{Name: svcAudit, State: fmt.Sprintf("%d events", audit.Summary.TotalEntries)})
			}

			if d.Printer.Format != FormatTable {
				return d.printStructured(rows)
			}
			cells := make([][]string, 0, len(rows))
			for _, row := range rows {
				cells = append(cells, []string{row.Name, row.State})
			}
			d.Printer.Table([]string{"SERVICE", colState}, cells)
			return nil
		},
	}
}

func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "MCP tool servers and their readiness",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d := deps(cmd)
			// /admin/health is the richer source: it alone carries last_error,
			// which /readyz withholds because it is unauthenticated and the
			// reason can quote a server URL or a token.
			servers, err := mcpServers(cmd)
			if err != nil {
				return err
			}
			if d.Printer.Format != FormatTable {
				return d.printStructured(servers)
			}
			if len(servers) == 0 {
				d.Printer.Warn("this gateway has no MCP servers configured")
			}
			rows := make([][]string, 0, len(servers))
			for _, s := range servers {
				rows = append(rows, []string{s.Name, table.BoolYN(s.Ready), table.BoolYN(s.Required), table.OrDash(s.LastError)})
			}
			d.Printer.Table([]string{colName, "READY", "REQUIRED", "LAST ERROR"}, rows)
			return nil
		},
	}
}

func mcpServers(cmd *cobra.Command) ([]api.MCPServer, error) {
	d := deps(cmd)
	ah, err := d.Client.AdminHealth(cmd.Context())
	if err == nil {
		return ah.MCPServers, nil
	}
	d.Printer.Warn("admin health unavailable (%v) — falling back to /readyz, which withholds last_error", err)
	ready, _, err := d.Client.Ready(cmd.Context())
	if err != nil {
		return nil, err
	}
	return ready.MCPServers, nil
}

// pluginRow merges the two listings: /admin/plugins says what this gateway has
// configured, /admin/plugins/catalog says what this build ships — and only the
// catalog knows whether a plugin fails open.
type pluginRow struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Enabled bool   `json:"enabled"`
	Fails   string `json:"fails"`
	Summary string `json:"summary,omitempty"`
}

func mergePlugins(configured []api.PluginInfo, catalog []api.BuiltinPlugin) []pluginRow {
	shipped := table.NewPluginCatalog(catalog)
	rows := make([]pluginRow, 0, len(configured))
	for _, p := range configured {
		policy := shipped.For(p.Name)
		rows = append(rows, pluginRow{
			Name: p.Name, Type: p.Type, Enabled: p.Enabled,
			Fails: policy.Fails, Summary: policy.Summary,
		})
	}
	return rows
}

func newPluginsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "plugins",
		Short: "Configured plugins, merged with this build's catalog",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d := deps(cmd)
			configured, err := d.Client.Plugins(cmd.Context())
			if err != nil {
				return err
			}
			catalog, err := d.Client.PluginCatalog(cmd.Context())
			if err != nil {
				// A build without the catalog route still lists what it runs;
				// it just cannot say what fails open.
				if !api.IsNotSupported(err) {
					return err
				}
				d.Printer.Warn("this gateway serves no plugin catalog — fail-open policy and summaries are unavailable")
				catalog = nil
			}
			rows := mergePlugins(configured, catalog)
			if d.Printer.Format != FormatTable {
				return d.printStructured(rows)
			}
			cells := make([][]string, 0, len(rows))
			for _, r := range rows {
				cells = append(cells, []string{r.Name, r.Type, table.BoolYN(r.Enabled), r.Fails, table.OrDash(r.Summary)})
			}
			d.Printer.Table([]string{colName, "TYPE", "ENABLED", "FAILS", "SUMMARY"}, cells)
			return nil
		},
	}
}

func newSessionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sessions",
		Short: "Active dashboard sessions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d := deps(cmd)
			sessions, err := d.Client.Sessions(cmd.Context())
			if err != nil {
				if api.IsNotSupported(err) {
					return fmt.Errorf("dashboard sessions are not enabled on this gateway, so there are none to list: %w", err)
				}
				return err
			}
			if d.Printer.Format != FormatTable {
				return d.printStructured(sessions)
			}
			rows := make([][]string, 0, len(sessions))
			for _, s := range sessions {
				rows = append(rows, []string{
					s.Subject, strings.Join(s.Scopes, ","),
					table.FmtTime(s.CreatedAt), table.FmtTimePtr(s.LastSeenAt), table.FmtTime(s.ExpiresAt),
				})
			}
			d.Printer.Table([]string{"SUBJECT", colScopes, "CREATED", "LAST SEEN", colExpires}, rows)
			return nil
		},
	}
}

func newAuditCmd() *cobra.Command {
	audit := &cobra.Command{
		Use:   "audit",
		Short: "Read the credential and configuration audit trail",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d := deps(cmd)
			q, err := auditQueryFromFlags(cmd)
			if err != nil {
				return err
			}
			page, err := d.Client.Audit(cmd.Context(), q)
			if err != nil {
				return err
			}
			if d.Printer.Format != FormatTable {
				return d.printStructured(page)
			}
			rows := make([][]string, 0, len(page.Data))
			for _, e := range page.Data {
				rows = append(rows, []string{
					table.FmtTime(e.OccurredAt), e.Action, table.OrDash(e.Actor), e.Outcome, table.OrDash(e.TargetID),
				})
			}
			d.Printer.Table([]string{colTime, "ACTION", "ACTOR", "OUTCOME", "TARGET"}, rows)
			return nil
		},
	}
	f := audit.Flags()
	f.String("action", "", "only this action (key.create, session.create, …)")
	f.String("actor", "", "only this actor's credential id")
	f.String("outcome", "", "ok|denied|error")
	f.String("since", "", "duration (15m, 2h) or an RFC3339 timestamp")
	f.Int("limit", 50, "rows to return (gateway caps at 200)")
	return audit
}

func auditQueryFromFlags(cmd *cobra.Command) (api.AuditQuery, error) {
	f := cmd.Flags()
	q := api.AuditQuery{}
	q.Action, _ = f.GetString("action")
	q.ActorID, _ = f.GetString("actor")
	q.Outcome, _ = f.GetString("outcome")
	q.Limit, _ = f.GetInt("limit")

	if q.Outcome != "" && !slices.Contains(auditOutcomes, q.Outcome) {
		return q, fmt.Errorf("outcome %q is not valid: --outcome must be one of %s, and the gateway answers 400 to anything else",
			q.Outcome, strings.Join(auditOutcomes, ", "))
	}
	if since, _ := f.GetString("since"); since != "" {
		t, err := api.ParseSince(since)
		if err != nil {
			return q, err
		}
		q.Since = t
	}
	return q, nil
}
