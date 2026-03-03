package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
)

// adminCmd is the root of the `admin` command group.
var adminCmd = &cobra.Command{
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
		c := clientFromCmd(cmd)
		var result interface{}
		if err := c.get("/admin/keys", &result); err != nil {
			return err
		}
		return newPrinter(formatFlag(cmd)).Print(&jsonSlice{
			headers: []string{"ID", "NAME", "SCOPE", "EXPIRES", "REVOKED"},
			data:    toSlice(result),
			rowFn: func(m map[string]interface{}) []string {
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
		c := clientFromCmd(cmd)
		var result interface{}
		if err := c.get("/admin/keys/"+args[0], &result); err != nil {
			return err
		}
		return newPrinter(formatFlag(cmd)).Print(result)
	},
}

var keysCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new API key",
	RunE: func(cmd *cobra.Command, _ []string) error {
		name, _ := cmd.Flags().GetString("name")
		scope, _ := cmd.Flags().GetString("scope")
		expiresIn, _ := cmd.Flags().GetString("expires-in")

		body := map[string]interface{}{
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

		c := clientFromCmd(cmd)
		var result interface{}
		if err := c.post("/admin/keys", body, &result); err != nil {
			return err
		}
		return newPrinter(formatFlag(cmd)).Print(result)
	},
}

var keysDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete (revoke) an API key",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := clientFromCmd(cmd)
		if err := c.del("/admin/keys/"+args[0], nil); err != nil {
			return err
		}
		printSuccess("Key deleted.")
		return nil
	},
}

var keysRotateCmd = &cobra.Command{
	Use:   "rotate <id>",
	Short: "Rotate an API key (generates a new key value)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := clientFromCmd(cmd)
		var result interface{}
		if err := c.post("/admin/keys/"+args[0]+"/rotate", nil, &result); err != nil {
			return err
		}
		return newPrinter(formatFlag(cmd)).Print(result)
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
		c := clientFromCmd(cmd)
		var result interface{}
		if err := c.get("/admin/config", &result); err != nil {
			return err
		}
		return newPrinter(formatFlag(cmd)).Print(result)
	},
}

var configHistoryCmd = &cobra.Command{
	Use:   "history",
	Short: "Show configuration change history",
	RunE: func(cmd *cobra.Command, _ []string) error {
		c := clientFromCmd(cmd)
		var result interface{}
		if err := c.get("/admin/config/history", &result); err != nil {
			return err
		}
		return newPrinter(formatFlag(cmd)).Print(&jsonSlice{
			headers: []string{"VERSION", "UPDATED_AT", "ROLLED_BACK_FROM"},
			data:    toSlice(result),
			rowFn: func(m map[string]interface{}) []string {
				rolledBack := ""
				if v, ok := m["rolled_back_from"]; ok && v != nil {
					rolledBack = fmt.Sprintf("%v", v)
				}
				return []string{fmt.Sprintf("%.0f", numVal(m, "version")), fmtTime(m, "updated_at"), rolledBack}
			},
		})
	},
}

var configUpdateCmd = &cobra.Command{
	Use:   "update",
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
		var body interface{}
		if err := json.Unmarshal(raw, &body); err != nil {
			return fmt.Errorf("parse config file: %w (only JSON is accepted by this command; convert YAML first)", err)
		}
		c := clientFromCmd(cmd)
		var result interface{}
		if err := c.put("/admin/config", body, &result); err != nil {
			return err
		}
		printSuccess("Configuration updated.")
		return nil
	},
}

var configRollbackCmd = &cobra.Command{
	Use:   "rollback <version>",
	Short: "Roll back to a previous configuration version",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := clientFromCmd(cmd)
		var result interface{}
		if err := c.post("/admin/config/rollback/"+args[0], nil, &result); err != nil {
			return err
		}
		printSuccess("Rolled back to version " + args[0] + ".")
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
		c := clientFromCmd(cmd)
		limit, _ := cmd.Flags().GetInt("limit")
		path := fmt.Sprintf("/admin/logs?limit=%d", limit)
		var result interface{}
		if err := c.get(path, &result); err != nil {
			return err
		}
		return newPrinter(formatFlag(cmd)).Print(&jsonSlice{
			headers: []string{"TRACE_ID", "PROVIDER", "MODEL", "STATUS", "LATENCY_MS", "TIMESTAMP"},
			data:    toSlice(result),
			rowFn: func(m map[string]interface{}) []string {
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
		c := clientFromCmd(cmd)
		var result interface{}
		if err := c.get("/admin/logs/stats", &result); err != nil {
			return err
		}
		return newPrinter(formatFlag(cmd)).Print(result)
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
		c := clientFromCmd(cmd)
		var result interface{}
		if err := c.get("/admin/providers", &result); err != nil {
			return err
		}
		return newPrinter(formatFlag(cmd)).Print(&jsonSlice{
			headers: []string{"PROVIDER", "MODELS"},
			data:    toSlice(result),
			rowFn: func(m map[string]interface{}) []string {
				return []string{str(m, "name"), fmt.Sprintf("%.0f", numVal(m, "model_count"))}
			},
		})
	},
}

var providersHealthCmd = &cobra.Command{
	Use:   "health",
	Short: "Show per-provider health status",
	RunE: func(cmd *cobra.Command, _ []string) error {
		c := clientFromCmd(cmd)
		var result interface{}
		if err := c.get("/admin/health", &result); err != nil {
			return err
		}
		return newPrinter(formatFlag(cmd)).Print(result)
	},
}

// ── Wire-up ───────────────────────────────────────────────────────────────────

func init() {
	// Keys sub-commands.
	keysCreateCmd.Flags().String("name", "", "Human-readable label for the key")
	keysCreateCmd.Flags().String("scope", "read_only", "Key scope: admin or read_only")
	keysCreateCmd.Flags().String("expires-in", "", "Expiry duration, e.g. 720h (30 days)")

	keysCmd.AddCommand(keysListCmd, keysGetCmd, keysCreateCmd, keysDeleteCmd, keysRotateCmd)

	// Config sub-commands.
	configUpdateCmd.Flags().String("file", "", "Path to JSON config file")
	configCmd.AddCommand(configGetCmd, configHistoryCmd, configUpdateCmd, configRollbackCmd)

	// Logs sub-commands.
	logsListCmd.Flags().Int("limit", 50, "Maximum number of log entries to return")
	logsCmd.AddCommand(logsListCmd, logsStatsCmd)

	// Providers sub-commands.
	providersCmd.AddCommand(providersListCmd, providersHealthCmd)

	// Register all groups.
	adminCmd.AddCommand(keysCmd, configCmd, logsCmd, providersCmd)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// clientFromCmd builds an adminClient from the persistent root flags.
func clientFromCmd(cmd *cobra.Command) *adminClient {
	url, _ := cmd.Root().PersistentFlags().GetString("gateway-url")
	key, _ := cmd.Root().PersistentFlags().GetString("api-key")
	return newAdminClient(url, key)
}

// formatFlag returns the value of the --format flag (table/json/yaml).
func formatFlag(cmd *cobra.Command) string {
	f, _ := cmd.Root().PersistentFlags().GetString("format")
	return f
}

// jsonSlice is a generic TableData wrapper around []map[string]interface{}.
type jsonSlice struct {
	headers []string
	data    []map[string]interface{}
	rowFn   func(map[string]interface{}) []string
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
func (j *jsonSlice) MarshalYAML() (interface{}, error) { return j.data, nil }

// toSlice converts an interface{} (decoded from JSON) to []map[string]interface{}.
// Handles both a JSON array and a single JSON object.
func toSlice(v interface{}) []map[string]interface{} {
	switch t := v.(type) {
	case []interface{}:
		out := make([]map[string]interface{}, 0, len(t))
		for _, item := range t {
			if m, ok := item.(map[string]interface{}); ok {
				out = append(out, m)
			}
		}
		return out
	case map[string]interface{}:
		return []map[string]interface{}{t}
	}
	return nil
}

// str safely extracts a string field from a map.
func str(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok && v != nil {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

// strBool prints "yes" or "no" for a boolean field.
func strBool(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if b, ok := v.(bool); ok && b {
			return "yes"
		}
	}
	return "no"
}

// numVal extracts a float64 from JSON number fields.
func numVal(m map[string]interface{}, key string) float64 {
	if v, ok := m[key]; ok {
		if f, ok := v.(float64); ok {
			return f
		}
	}
	return 0
}

// fmtTime parses an RFC3339 timestamp field and returns a short human form.
func fmtTime(m map[string]interface{}, key string) string {
	s := str(m, key)
	if s == "" {
		return "—"
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return s
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}
