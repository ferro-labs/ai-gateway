package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spf13/cobra"
)

// newHandlerCmd builds a child command mounted under a root carrying the
// gateway-url/api-key/format persistent flags (as cmd/ferrogw/main.go wires
// them), with the child's output and error writers pointed at a buffer so a
// handler called directly (e.g. runStatus(child, nil)) can be asserted against
// its output. NO_COLOR is forced so assertions ignore ANSI codes, and the
// FERROGW_*/MASTER_KEY env vars are cleared so only the flags under test drive
// client construction. Callers are therefore serial (t.Setenv).
func newHandlerCmd(t *testing.T, gatewayURL, format string) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	t.Setenv("NO_COLOR", "1")
	t.Setenv("FERROGW_URL", "")
	t.Setenv("FERROGW_API_KEY", "")
	t.Setenv("MASTER_KEY", "")

	root := &cobra.Command{Use: "ferrogw"}
	root.PersistentFlags().String("gateway-url", gatewayURL, "")
	root.PersistentFlags().String("api-key", "", "")
	root.PersistentFlags().String("format", format, "")

	child := &cobra.Command{Use: "child"}
	root.AddCommand(child)
	// Handlers are called directly here (not via Execute), so cmd.Context()
	// would otherwise be nil; set it so request building has a context.
	child.SetContext(context.Background())

	buf := &bytes.Buffer{}
	child.SetOut(buf)
	child.SetErr(buf)
	return child, buf
}

// stubGateway starts an httptest server routing the given path patterns and
// tears it down via t.Cleanup so it is closed even when the test fails.
func stubGateway(t *testing.T, routes map[string]http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for pattern, h := range routes {
		mux.HandleFunc(pattern, h)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// jsonHandler responds with a fixed status and JSON body.
func jsonHandler(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}
