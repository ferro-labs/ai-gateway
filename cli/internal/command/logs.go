package command

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ferro-labs/ai-gateway/cli/internal/api"
	"github.com/ferro-labs/ai-gateway/cli/internal/table"
)

const (
	// tailInterval is the poll cadence of `logs tail`; tailMaxInterval caps the
	// ×2 backoff a failing gateway earns.
	tailInterval    = 2 * time.Second
	tailMaxInterval = 30 * time.Second

	// metricCostUSD is the one spelling of the cost total: `logs stats` prints
	// it as a row name and the JSON form of the same report carries it as a
	// field, so a script greps one name whichever --format it asked for.
	metricCostUSD = "cost_usd"

	// tailLookback is the window the first poll covers when no --since was
	// given. It is a cursor, not a report: enough history that a row written
	// while the command was starting is not missed, little enough that a bare
	// tail does not replay the gateway's whole default window as though it had
	// just happened.
	tailLookback = 2 * time.Minute
)

func newLogsCmd() *cobra.Command {
	logs := &cobra.Command{
		Use:   "logs",
		Short: "Read the gateway request log",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	logs.AddCommand(newLogsListCmd(), newLogsStatsCmd(), newLogsTailCmd())
	return logs
}

// addLogFilterFlags installs the filter set `list` and `tail` share, so a flag
// cannot mean one thing in one verb and another in the next.
func addLogFilterFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.String("since", "", "duration (15m, 2h) or an RFC3339 timestamp")
	f.String("model", "", "only rows for this model")
	f.String("provider", "", "only rows served by this provider")
	f.String("stage", "", "pipeline stage; 'all' for every stage (default: terminal stages only)")
	f.String("api-key-id", "", "only rows for this credential id ('none' for credential-less rows)")
}

func addLogStatsFilterFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.String("since", "", "duration (15m, 2h) or an RFC3339 timestamp")
	f.String("model", "", "only rows for this model")
	f.String("provider", "", "only rows served by this provider")
	f.String("stage", "", "only rows for this pipeline stage")
}

func logsQueryFromFlags(cmd *cobra.Command) (api.LogsQuery, error) {
	f := cmd.Flags()
	q := api.LogsQuery{}
	q.Model, _ = f.GetString("model")
	q.Provider, _ = f.GetString("provider")
	q.Stage, _ = f.GetString("stage")
	q.APIKeyID, _ = f.GetString("api-key-id")

	if since, _ := f.GetString("since"); since != "" {
		t, err := api.ParseSince(since)
		if err != nil {
			return q, err
		}
		q.Since = t
	}
	// list-only paging flags; absent on tail, where they make no sense.
	if f.Lookup("limit") != nil {
		q.Limit, _ = f.GetInt("limit")
	}
	if f.Lookup("offset") != nil {
		q.Offset, _ = f.GetInt("offset")
	}
	return q, nil
}

// noStore turns a 501 into the sentence an operator can act on. The raw error
// stays wrapped for anyone who needs the status code.
func noStore(err error) error {
	if api.IsNotSupported(err) {
		return fmt.Errorf("this gateway has no request-log store configured, so there are no request logs to read (set REQUEST_LOG_STORE_BACKEND and REQUEST_LOG_STORE_DSN): %w", err)
	}
	return err
}

func newLogsListCmd() *cobra.Command {
	list := &cobra.Command{
		Use:   verbList,
		Short: "List request-log rows",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d := deps(cmd)
			q, err := logsQueryFromFlags(cmd)
			if err != nil {
				return err
			}
			page, err := d.Client.Logs(cmd.Context(), q)
			if err != nil {
				return noStore(err)
			}
			if d.Printer.Format != FormatTable {
				return d.printStructured(page)
			}
			rows := make([][]string, 0, len(page.Data))
			for _, e := range page.Data {
				rows = append(rows, []string{
					table.FmtTime(e.CreatedAt), e.TraceID, table.OrDash(e.Provider), table.OrDash(e.Model),
					e.Stage, fmtMillis(e.DurationMs), fmtCost(e.CostUSD),
					fmt.Sprintf("%d", e.TotalTokens),
				})
			}
			d.Printer.Table([]string{colTime, "TRACE", colProvider, "MODEL", "STAGE", "MS", "COST", "TOKENS"}, rows)
			return nil
		},
	}
	addLogFilterFlags(list)
	list.Flags().Int("limit", 50, "rows to return (gateway caps at 200)")
	list.Flags().Int("offset", 0, "rows to skip")
	return list
}

func newLogsStatsCmd() *cobra.Command {
	stats := &cobra.Command{
		Use:   "stats",
		Short: "Aggregate request-log statistics (totals, errors, latency percentiles)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d := deps(cmd)
			q, err := logsQueryFromFlags(cmd)
			if err != nil {
				return err
			}
			s, err := d.Client.LogStats(cmd.Context(), q)
			if err != nil {
				return noStore(err)
			}
			if d.Printer.Format != FormatTable {
				return d.printStructured(s)
			}
			sum := s.Summary
			rows := slices.Concat(
				[][]string{
					{"requests", fmt.Sprintf("%d", sum.TotalEntries)},
					{"errors", fmt.Sprintf("%d", sum.ErrorEntries)},
					{"error_rate", fmtRate(sum.ErrorEntries, sum.TotalEntries)},
					{"tokens", fmt.Sprintf("%d", sum.TotalTokens)},
					{"prompt_tokens", fmt.Sprintf("%d", sum.PromptTokens)},
					{"completion_tokens", fmt.Sprintf("%d", sum.CompletionTokens)},
					{metricCostUSD, fmt.Sprintf("%.4f", sum.CostUSD)},
					{"unpriced_requests", fmt.Sprintf("%d", sum.UnpricedRequests)},
				},
				percentileRows("latency", s.LatencyMs),
				percentileRows("ttft", s.TTFTMs),
			)
			d.Printer.Table([]string{"METRIC", "VALUE"}, rows)
			return nil
		},
	}
	addLogStatsFilterFlags(stats)
	return stats
}

// percentileRows renders one distribution. A nil block means nothing was
// measured, which is a "-" and not a zero-latency gateway.
func percentileRows(prefix string, p *api.Percentiles) [][]string {
	// The unmeasured table, which is also the answer when p is nil.
	rows := [][]string{
		{prefix + "_p50_ms", "-"},
		{prefix + "_p95_ms", "-"},
		{prefix + "_p99_ms", "-"},
		{prefix + "_max_ms", "-"},
	}
	if p == nil {
		return rows
	}
	for i, v := range []float64{p.P50, p.P95, p.P99, p.Max} {
		rows[i][1] = fmt.Sprintf("%.1f", v)
	}
	return rows
}

func newLogsTailCmd() *cobra.Command {
	tail := &cobra.Command{
		Use:   "tail",
		Short: "Follow the request log (one plain line per new row)",
		Long: "tail polls the request log every 2s and prints each new row as a line.\n" +
			"A failing poll backs off ×2 to 30s and keeps the last cursor, so no row\n" +
			"is skipped. Interrupting a tail is a clean exit, not a failure.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d := deps(cmd)
			q, err := logsQueryFromFlags(cmd)
			if err != nil {
				return err
			}
			if q.Since.IsZero() {
				q.Since = time.Now().Add(-tailLookback)
			}
			f := api.NewFollower(d.Client, q)
			delay := tailInterval
			for {
				rows, err := f.Poll(cmd.Context())
				// A truncated poll is progress, not a failure: its rows are
				// complete and its cursor moved past them, so they are printed,
				// the cadence stays, and only the older end it could not reach
				// is reported — on stderr, where every loss is stated.
				truncated := errors.Is(err, api.ErrFollowTruncated)
				switch {
				case err == nil || truncated:
					delay = tailInterval
					if truncated {
						d.Printer.Warn("%v", err)
					}
					for _, e := range rows {
						_, _ = fmt.Fprintln(d.Printer.Out, formatLogLine(e))
					}
				case cmd.Context().Err() != nil:
					// The failing poll is the interrupt itself, not a fault.
					return nil
				case api.IsNotSupported(err):
					// A feature that is absent will not appear by retrying.
					return noStore(err)
				default:
					// Backed off before it is reported: the warning names the
					// wait the operator is about to sit through, not the one
					// that has already elapsed.
					delay = backoff(delay)
					d.Printer.Warn("poll failed (%v) — retrying in %s", err, delay)
				}
				select {
				case <-cmd.Context().Done():
					return nil // an interrupted tail is a clean exit
				case <-time.After(delay):
				}
			}
		},
	}
	addLogFilterFlags(tail)
	return tail
}

func backoff(d time.Duration) time.Duration {
	return min(d*2, tailMaxInterval)
}

// formatLogLine is the tail's one-row-per-line rendering. Fields are
// space-separated and normalized to one physical line, so `awk` still works;
// the free-form error message remains last. Every fixed-width field goes
// through oneField, not only the error message: a Provider, Model or Stage
// the gateway sent with an embedded space or newline would otherwise widen
// into an extra column, and a newline in any field would break the
// one-physical-line contract `awk` depends on.
func formatLogLine(e api.LogEntry) string {
	fields := []string{
		oneField(table.FmtTime(e.CreatedAt)),
		oneField(e.TraceID),
		oneField(table.OrDash(e.Provider)),
		oneField(table.OrDash(e.Model)),
		oneField(e.Stage),
		oneField(fmtMillis(e.DurationMs)),
		oneField(fmtCost(e.CostUSD)),
		fmt.Sprintf("%d", e.TotalTokens),
	}
	line := strings.Join(fields, " ")
	if e.ErrorMessage != "" {
		line += " " + oneLine(e.ErrorMessage)
	}
	return line
}

// oneField collapses s to a single whitespace-free token, joining any
// embedded words with "_" rather than a space, so the value can never widen
// into more than the one column it occupies in formatLogLine's fixed-width
// prefix. A value that normalizes to nothing (empty, or all whitespace)
// becomes "-": an empty column still occupies a position in the joined
// string but disappears under an `awk`-style whitespace split, silently
// shifting every column after it.
func oneField(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return "-"
	}
	return strings.Join(f, "_")
}

// oneLine collapses s's internal whitespace runs to single spaces. Used only
// for the trailing, free-form error message, which may keep its internal
// spaces because it is always the last thing on the row -- past every column
// a script indexes into by position -- and only a literal newline or tab
// inside it can break the one-line contract.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// fmtMillis and fmtCost keep the nullable-measurement rule in one place: the
// gateway sends null for "not measured", and a rendered 0 would claim a real
// instantaneous or free request.
func fmtMillis(v *float64) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%.1f", *v)
}

func fmtCost(v *float64) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("$%.4f", *v)
}

func fmtRate(part, total int) string {
	if total == 0 {
		return "-"
	}
	return fmt.Sprintf("%.2f%%", 100*float64(part)/float64(total))
}
