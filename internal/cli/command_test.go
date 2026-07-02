package cli

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/spf13/cobra"
)

// newTestCmd builds a root command with a child subcommand, wiring up the
// gateway-url/api-key/format persistent flags the same way cmd/ferrogw/main.go
// does, then returns the child so callers can exercise cmd.Root() lookups.
func newTestCmd() (root, child *cobra.Command) {
	root = &cobra.Command{Use: "ferrogw"}
	child = &cobra.Command{Use: "child"}
	root.AddCommand(child)

	root.PersistentFlags().String("gateway-url", "", "Gateway base URL")
	root.PersistentFlags().String("api-key", "", "Admin API key")
	root.PersistentFlags().String("format", "table", "Output format")

	return root, child
}

func TestAdminClientFromCmd(t *testing.T) {
	// Arrange: clear env vars so flag values (or their absence) are what's
	// under test, not host environment leakage.
	t.Setenv("FERROGW_URL", "")
	t.Setenv("FERROGW_API_KEY", "")
	t.Setenv("MASTER_KEY", "")

	tests := []struct {
		name        string
		gatewayURL  string
		apiKey      string
		wantBaseURL string
		wantAPIKey  string
	}{
		{
			name:        "reads explicit gateway-url and api-key flags",
			gatewayURL:  "https://gw.example.com",
			apiKey:      "sk-test-123",
			wantBaseURL: "https://gw.example.com",
			wantAPIKey:  "sk-test-123",
		},
		{
			name:        "falls back to default base URL when gateway-url is empty",
			gatewayURL:  "",
			apiKey:      "",
			wantBaseURL: "http://localhost:8080",
			wantAPIKey:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			_, child := newTestCmd()
			if err := child.Root().PersistentFlags().Set("gateway-url", tt.gatewayURL); err != nil {
				t.Fatalf("set gateway-url: %v", err)
			}
			if err := child.Root().PersistentFlags().Set("api-key", tt.apiKey); err != nil {
				t.Fatalf("set api-key: %v", err)
			}

			// Act
			client := adminClientFromCmd(child)

			// Assert
			if client == nil {
				t.Fatal("adminClientFromCmd returned nil")
			}
			if client.BaseURL != tt.wantBaseURL {
				t.Errorf("BaseURL = %q, want %q", client.BaseURL, tt.wantBaseURL)
			}
			if client.APIKey != tt.wantAPIKey {
				t.Errorf("APIKey = %q, want %q", client.APIKey, tt.wantAPIKey)
			}
		})
	}
}

func TestAdminClientFromCmd_MissingFlags(t *testing.T) {
	// Arrange: a command with no gateway-url/api-key persistent flags
	// registered at all (e.g. a bare command not wired via main.go).
	t.Setenv("FERROGW_URL", "")
	t.Setenv("FERROGW_API_KEY", "")
	t.Setenv("MASTER_KEY", "")
	cmd := &cobra.Command{Use: "standalone"}

	// Act
	client := adminClientFromCmd(cmd)

	// Assert: missing flags should not panic and should resolve to the
	// documented defaults instead of propagating a flag-lookup error.
	if client == nil {
		t.Fatal("adminClientFromCmd returned nil")
	}
	if client.BaseURL != "http://localhost:8080" {
		t.Errorf("BaseURL = %q, want default %q", client.BaseURL, "http://localhost:8080")
	}
	if client.APIKey != "" {
		t.Errorf("APIKey = %q, want empty", client.APIKey)
	}
}

// captureStdout redirects os.Stdout for the duration of fn and returns
// whatever was written to it.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	os.Stdout = orig

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	return buf.String()
}

func TestPrintResult(t *testing.T) {
	// jsonSlice (defined in admin.go) implements TableData, so it exercises
	// both the table branch and the JSON/YAML encoder branches.
	data := &jsonSlice{
		headers: []string{"NAME"},
		data:    []map[string]any{{"name": "openai"}},
		rowFn: func(m map[string]any) []string {
			return []string{str(m, "name")}
		},
	}

	tests := []struct {
		name       string
		format     string
		wantSubstr string
	}{
		{name: "table format renders headers", format: FormatTable, wantSubstr: "NAME"},
		{name: "json format renders payload", format: FormatJSON, wantSubstr: "openai"},
		{name: "yaml format renders payload", format: FormatYAML, wantSubstr: "openai"},
		{name: "unrecognized format falls back to table", format: "bogus", wantSubstr: "NAME"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			_, child := newTestCmd()
			if err := child.Root().PersistentFlags().Set("format", tt.format); err != nil {
				t.Fatalf("set format: %v", err)
			}

			// Act
			var runErr error
			out := captureStdout(t, func() {
				runErr = printResult(child, data)
			})

			// Assert
			if runErr != nil {
				t.Fatalf("printResult returned error: %v", runErr)
			}
			if !bytes.Contains([]byte(out), []byte(tt.wantSubstr)) {
				t.Errorf("output = %q, want substring %q", out, tt.wantSubstr)
			}
		})
	}
}

func TestPrintResult_MissingFormatFlag(t *testing.T) {
	// Arrange: no format flag registered at all — printResult should still
	// route to NewPrinter's default (table) rather than error out.
	cmd := &cobra.Command{Use: "standalone"}
	data := &jsonSlice{
		headers: []string{"NAME"},
		data:    []map[string]any{{"name": "anthropic"}},
		rowFn: func(m map[string]any) []string {
			return []string{str(m, "name")}
		},
	}

	// Act
	var runErr error
	out := captureStdout(t, func() {
		runErr = printResult(cmd, data)
	})

	// Assert
	if runErr != nil {
		t.Fatalf("printResult returned error: %v", runErr)
	}
	if !bytes.Contains([]byte(out), []byte("NAME")) {
		t.Errorf("output = %q, want table header %q", out, "NAME")
	}
}
