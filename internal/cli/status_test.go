package cli

import (
	"net/http"
	"strings"
	"testing"
)

func TestRunStatus(t *testing.T) {
	t.Run("healthy gateway reports version and providers", func(t *testing.T) {
		srv := stubGateway(t, map[string]http.HandlerFunc{
			"/health":          jsonHandler(http.StatusOK, `{"status":"ok","version":"1.2.0"}`),
			"/admin/providers": jsonHandler(http.StatusOK, `[{"name":"openai","models":["gpt-4","gpt-4o"]}]`),
		})
		cmd, out := newHandlerCmd(t, srv.URL, "table")

		if err := runStatus(cmd, nil); err != nil {
			t.Fatalf("runStatus: %v", err)
		}

		got := out.String()
		for _, want := range []string{"healthy", "1.2.0", "Providers", "2 models"} {
			if !strings.Contains(got, want) {
				t.Errorf("output missing %q:\n%s", want, got)
			}
		}
	})

	t.Run("degraded gateway shows status rather than unreachable", func(t *testing.T) {
		srv := stubGateway(t, map[string]http.HandlerFunc{
			"/health": jsonHandler(http.StatusServiceUnavailable, `{"status":"no_providers"}`),
		})
		cmd, out := newHandlerCmd(t, srv.URL, "table")

		if err := runStatus(cmd, nil); err != nil {
			t.Fatalf("runStatus: %v", err)
		}

		got := out.String()
		if !strings.Contains(got, "no_providers") {
			t.Errorf("want degraded status in output, got:\n%s", got)
		}
		if strings.Contains(got, "unreachable") {
			t.Errorf("a degraded gateway must not be reported unreachable:\n%s", got)
		}
	})

	t.Run("unreachable gateway is reported without an error", func(t *testing.T) {
		// Port 1 refuses connections immediately.
		cmd, out := newHandlerCmd(t, "http://127.0.0.1:1", "table")

		if err := runStatus(cmd, nil); err != nil {
			t.Fatalf("runStatus should not error for an unreachable gateway: %v", err)
		}
		if !strings.Contains(out.String(), "unreachable") {
			t.Errorf("want 'unreachable', got:\n%s", out.String())
		}
	})
}
