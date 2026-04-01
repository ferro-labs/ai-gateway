package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
)

// AdminCmd is the root of the `admin` command group.
var AdminCmd = &cobra.Command{
	Use:   "admin",
	Short: "Manage a running Ferro Labs AI Gateway instance",
	Long: `Manage a running gateway over its Admin API.

Set the gateway URL and API key via flags or environment variables:
  FERROGW_URL      Gateway base URL  (default: http://localhost:8080)
  FERROGW_API_KEY  Admin API key`,
}

// ── Keys ──────────────────────────────────────────────────────────────────────

var keysCmd = &cobra.Command{
	Use:   "keys",
	Short: "Manage API keys",
}

var keysListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all API keys",
	RunE: func(cmd *cobra.Command, _ []string) error {
		flagURL, _ := cmd.Root().PersistentFlags().GetString("gateway-url")
		flagKey, _ := cmd.Root().PersistentFlags().GetString("api-key")
		c := NewAdminClient(flagURL, flagKey)
		var result any
		if err := c.Get("/admin/keys", &result); err != nil {
			return err
		}
		format, _ := cmd.Root().PersistentFlags().GetString("format")
		pr := NewPrinter(format)
		return pr.Print(&jsonSlice{
			headers: []string{"ID", "NAME", "SCOPE", "EXPIRES", "REVOKED"},
			data:    toSlice(result),
			rowFn: func(m map[string]any) []string {
				return []string{
					str(m, "id"), str(m, "name"), str(m, "scope"),
					fmtTime(m, "expires_at"), strBool(m, "revoked"),
				}
			},
		})
	},
}

var keysGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Get details of an API key",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		flagURL, _ := cmd.Root().PersistentFlags().GetString("gateway-url")
		flagKey, _ := cmd.Root().PersistentFlags().GetString("api-key")
		c := NewAdminClient(flagURL, flagKey)
		var result any
		if err := c.Get("/admin/keys/"+args[0], &result); err != nil {
			return err
		}
		format, _ := cmd.Root().PersistentFlags().GetString("format")
		pr := NewPrinter(format)
		return pr.Print(result)
	},
}

var keysCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new API key",
	RunE: func(cmd *cobra.Command, _ []string) error {
		name, _ := cmd.Flags().GetString("name")
		scope, _ := cmd.Flags().GetString("scope")
		expiresIn, _ := cmd.Flags().GetString("expires-in")

		body := map[string]any{
			"name":  name,
			"scope": scope,
		}
		if expiresIn != "" {
			d, err := time.ParseDuration(expiresIn)
			if err != nil {
				return fmt.Errorf("invalid --expires-in duration: %w", err)
			}
			body["expires_at"] = time.Now().UTC().Add(d).Format(time.RFC3339)
		}

		flagURL, _ := cmd.Root().PersistentFlags().GetString("gateway-url")
		flagKey, _ := cmd.Root().PersistentFlags().GetString("api-key")
		c := NewAdminClient(flagURL, flagKey)
		var result any
		if err := c.Post("/admin/keys", body, &result); err != nil {
			return err
		}
		format, _ := cmd.Root().PersistentFlags().GetString("format")
		pr := NewPrinter(format)
		return pr.Print(result)
	},
}

var keysRevokeCmd = &cobra.Command{
	Use:   "revoke <id>",
	Short: "Revoke an API key",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		flagURL, _ := cmd.Root().PersistentFlags().GetString("gateway-url")
		flagKey, _ := cmd.Root().PersistentFlags().GetString("api-key")
		c := NewAdminClient(flagURL, flagKey)
		if err := c.Del("/admin/keys/"+args[0], nil); err != nil {
			return err
		}
		PrintSuccess("Key revoked.")
		return nil
	},
}

var keysRotateCmd = &cobra.Command{
	Use:   "rotate <id>",
	Short: "Rotate an API key (generates a new key value)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		flagURL, _ := cmd.Root().PersistentFlags().GetString("gateway-url")
		flagKey, _ := cmd.Root().PersistentFlags().GetString("api-key")
		c := NewAdminClient(flagURL, flagKey)
		var result any
		if err := c.Post("/admin/keys/"+args[0]+"/rotate", nil, &result); err != nil {
			return err
		}
		format, _ := cmd.Root().PersistentFlags().GetString("format")
		pr := NewPrinter(format)
		return pr.Print(result)
	},
}

// ── Config ───────────────────────────────────────────────────────────────────

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage gateway configuration",
}

var configGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Print the current runtime configuration",
	RunE: func(cmd *cobra.Command, _ []string) error {
		flagURL, _ := cmd.Root().PersistentFlags().GetString("gateway-url")
		flagKey, _ := cmd.Root().PersistentFlags().GetString("api-key")
		c := NewAdminClient(flagURL, flagKey)
		var result any
		if err := c.Get("/admin/config", &result); err != nil {
			return err
		}
		format, _ := cmd.Root().PersistentFlags().GetString("format")
		pr := NewPrinter(format)
		return pr.Print(result)
	},
}

var configHistoryCmd = &cobra.Command{
	Use:   "history",
	Short: "Show configuration change history",
	RunE: func(cmd *cobra.Command, _ []string) error {
		flagURL, _ := cmd.Root().PersistentFlags().GetString("gateway-url")
		flagKey, _ := cmd.Root().PersistentFlags().GetString("api-key")
		c := NewAdminClient(flagURL, flagKey)
		var result any
		if err := c.Get("/admin/config/history", &result); err != nil {
			return err
		}
		format, _ := cmd.Root().PersistentFlags().GetString("format")
		pr := NewPrinter(format)
		return pr.Print(&jsonSlice{
			headers: []string{"VERSION", "UPDATED_AT", "ROLLED_BACK_FROM"},
			data:    toSlice(result),
			rowFn: func(m map[string]any) []string {
				rolledBack := ""
				if v, ok := m["rolled_back_from"]; ok && v != nil {
					rolledBack = fmt.Sprintf("%v", v)
				}
				return []string{fmt.Sprintf("%.0f", numVal(m, "version")), fmtTime(m, "updated_at"), rolledBack}
			},
		})
	},
}

var configSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Apply a new configuration (JSON file)",
	RunE: func(cmd *cobra.Command, _ []string) error {
		filePath, _ := cmd.Flags().GetString("file")
		if filePath == "" {
			return fmt.Errorf("--file is required")
		}
		raw, err := os.ReadFile(filePath) //nolint:gosec
		if err != nil {
			return fmt.Errorf("read file: %w", err)
		}
		// Decode locally so we send JSON regardless of input format.
		var body any
		if err := json.Unmarshal(raw, &body); err != nil {
			return fmt.Errorf("parse config file: %w (only JSON is accepted by this command; convert YAML first)", err)
		}
		flagURL, _ := cmd.Root().PersistentFlags().GetString("gateway-url")
		flagKey, _ := cmd.Root().PersistentFlags().GetString("api-key")
		c := NewAdminClient(flagURL, flagKey)
		var result any
		if err := c.Put("/admin/config", body, &result); err != nil {
			return err
		}
		PrintSuccess("Configuration updated.")
		return nil
	},
}

var configRollbackCmd = &cobra.Command{
	Use:   "rollback <version>",
	Short: "Roll back to a previous configuration version",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		flagURL, _ := cmd.Root().PersistentFlags().GetString("gateway-url")
		flagKey, _ := cmd.Root().PersistentFlags().GetString("api-key")
		c := NewAdminClient(flagURL, flagKey)
		var result any
		if err := c.Post("/admin/config/rollback/"+args[0], nil, &result); err != nil {
			return err
		}
		PrintSuccess("Rolled back to version " + args[0] + ".")
		return nil
	},
}

// ── Logs ─────────────────────────────────────────────────────────────────────

var logsCmd = &cobra.Command{
	Use:   "logs",
	Short: "View request logs",
}

var logsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List persisted request logs",
	RunE: func(cmd *cobra.Command, _ []string) error {
		flagURL, _ := cmd.Root().PersistentFlags().GetString("gateway-url")
		flagKey, _ := cmd.Root().PersistentFlags().GetString("api-key")
		c := NewAdminClient(flagURL, flagKey)
		limit, _ := cmd.Flags().GetInt("limit")
		path := fmt.Sprintf("/admin/logs?limit=%d", limit)
		var result any
		if err := c.Get(path, &result); err != nil {
			return err
		}
		format, _ := cmd.Root().PersistentFlags().GetString("format")
		pr := NewPrinter(format)
		return pr.Print(&jsonSlice{
			headers: []string{"TRACE_ID", "PROVIDER", "MODEL", "STATUS", "LATENCY_MS", "TIMESTAMP"},
			data:    toSlice(result),
			rowFn: func(m map[string]any) []string {
				return []string{
					str(m, "trace_id"), str(m, "provider"), str(m, "model"),
					fmt.Sprintf("%.0f", numVal(m, "status")),
					fmt.Sprintf("%.0f", numVal(m, "latency_ms")),
					str(m, "timestamp"),
				}
			},
		})
	},
}

var logsStatsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show aggregated log statistics",
	RunE: func(cmd *cobra.Command, _ []string) error {
		flagURL, _ := cmd.Root().PersistentFlags().GetString("gateway-url")
		flagKey, _ := cmd.Root().PersistentFlags().GetString("api-key")
		c := NewAdminClient(flagURL, flagKey)
		var result any
		if err := c.Get("/admin/logs/stats", &result); err != nil {
			return err
		}
		format, _ := cmd.Root().PersistentFlags().GetString("format")
		pr := NewPrinter(format)
		return pr.Print(result)
	},
}

// ── Providers ────────────────────────────────────────────────────────────────

var providersCmd = &cobra.Command{
	Use:   "providers",
	Short: "Inspect registered providers",
}

var providersListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all registered providers and their model counts",
	RunE: func(cmd *cobra.Command, _ []string) error {
		flagURL, _ := cmd.Root().PersistentFlags().GetString("gateway-url")
		flagKey, _ := cmd.Root().PersistentFlags().GetString("api-key")
		c := NewAdminClient(flagURL, flagKey)
		var result any
		if err := c.Get("/admin/providers", &result); err != nil {
			return err
		}
		format, _ := cmd.Root().PersistentFlags().GetString("format")
		pr := NewPrinter(format)
		return pr.Print(&jsonSlice{
			headers: []string{"PROVIDER", "MODELS"},
			data:    toSlice(result),
			rowFn: func(m map[string]any) []string {
				return []string{str(m, "name"), fmt.Sprintf("%.0f", numVal(m, "model_count"))}
			},
		})
	},
}

var providersHealthCmd = &cobra.Command{
	Use:   "health",
	Short: "Show per-provider health status",
	RunE: func(cmd *cobra.Command, _ []string) error {
		flagURL, _ := cmd.Root().PersistentFlags().GetString("gateway-url")
		flagKey, _ := cmd.Root().PersistentFlags().GetString("api-key")
		c := NewAdminClient(flagURL, flagKey)
		var result any
		if err := c.Get("/admin/health", &result); err != nil {
			return err
		}
		format, _ := cmd.Root().PersistentFlags().GetString("format")
		pr := NewPrinter(format)
		return pr.Print(result)
	},
}

// ── Wire-up ───────────────────────────────────────────────────────────────────

func init() {
	// Keys sub-commands.
	keysCreateCmd.Flags().String("name", "", "Human-readable label for the key")
	keysCreateCmd.Flags().String("scope", "read_only", "Key scope: admin or read_only")
	keysCreateCmd.Flags().String("expires-in", "", "Expiry duration, e.g. 720h (30 days)")

	keysCmd.AddCommand(keysListCmd, keysGetCmd, keysCreateCmd, keysRevokeCmd, keysRotateCmd)

	// Config sub-commands.
	configSetCmd.Flags().String("file", "", "Path to JSON config file")
	configCmd.AddCommand(configGetCmd, configHistoryCmd, configSetCmd, configRollbackCmd)

	// Logs sub-commands.
	logsListCmd.Flags().Int("limit", 50, "Maximum number of log entries to return")
	logsCmd.AddCommand(logsListCmd, logsStatsCmd)

	// Providers sub-commands.
	providersCmd.AddCommand(providersListCmd, providersHealthCmd)

	// Register all groups.
	AdminCmd.AddCommand(keysCmd, configCmd, logsCmd, providersCmd)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// jsonSlice is a generic TableData wrapper around []map[string]any.
type jsonSlice struct {
	headers []string
	data    []map[string]any
	rowFn   func(map[string]any) []string
}

func (j *jsonSlice) Headers() []string { return j.headers }
func (j *jsonSlice) Rows() [][]string {
	rows := make([][]string, 0, len(j.data))
	for _, m := range j.data {
		rows = append(rows, j.rowFn(m))
	}
	return rows
}

// MarshalJSON so Print(jsonSlice) emits the underlying slice as JSON.
func (j *jsonSlice) MarshalJSON() ([]byte, error) { return json.Marshal(j.data) }

// MarshalYAML so Print(jsonSlice) emits the underlying slice as YAML.
// Without this, gopkg.in/yaml.v3 reflects over unexported fields and produces {}.
func (j *jsonSlice) MarshalYAML() (any, error) { return j.data, nil }

// toSlice converts an any (decoded from JSON) to []map[string]any.
// Handles both a JSON array and a single JSON object.
func toSlice(v any) []map[string]any {
	switch t := v.(type) {
	case []any:
		out := make([]map[string]any, 0, len(t))
		for _, item := range t {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	case map[string]any:
		return []map[string]any{t}
	}
	return nil
}

// str safely extracts a string field from a map.
func str(m map[string]any, key string) string {
	if v, ok := m[key]; ok && v != nil {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

// strBool prints "yes" or "no" for a boolean field.
func strBool(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if b, ok := v.(bool); ok && b {
			return "yes"
		}
	}
	return "no"
}

// numVal extracts a float64 from JSON number fields.
func numVal(m map[string]any, key string) float64 {
	if v, ok := m[key]; ok {
		if f, ok := v.(float64); ok {
			return f
		}
	}
	return 0
}

// fmtTime parses an RFC3339 timestamp field and returns a short human form.
func fmtTime(m map[string]any, key string) string {
	s := str(m, key)
	if s == "" {
		return "-"
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return s
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}
