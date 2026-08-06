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

	// OPS-006. This subtest previously asserted the opposite — that an
	// unreachable gateway is reported and exits 0 — which made `ferrogw status
	// || alert` blind to a down gateway and put the failure text on stdout,
	// where a caller piping to jq reads it. The contract is deliberately
	// reversed: unreachable is an error, degraded-but-answering is not.
	t.Run("unreachable gateway is an error and writes nothing to stdout", func(t *testing.T) {
		// Port 1 refuses connections immediately.
		cmd, out := newHandlerCmd(t, "http://127.0.0.1:1", "table")

		err := runStatus(cmd, nil)
		if err == nil {
			t.Fatal("runStatus must error for an unreachable gateway so a caller's exit code sees it")
		}
		if !strings.Contains(err.Error(), "unreachable") {
			t.Errorf("error = %v, want it to say the gateway is unreachable", err)
		}
		// The diagnostic belongs on stderr, which cobra writes the returned
		// error to. stdout is the machine channel.
		if out.String() != "" {
			t.Errorf("want no stdout output for an unreachable gateway, got:\n%s", out.String())
		}
	})

	// --format is a root persistent flag, so cobra advertises it here too, and
	// it was silently ignored: `ferrogw status --format json | jq` received
	// ANSI-decorated prose. There is no useful JSON encoding of a health
	// narrative, so the flag is refused rather than faked.
	t.Run("a non-table --format is refused rather than ignored", func(t *testing.T) {
		srv := stubGateway(t, map[string]http.HandlerFunc{
			"/health": jsonHandler(http.StatusOK, `{"status":"ok"}`),
		})
		cmd, out := newHandlerCmd(t, srv.URL, "json")

		err := runStatus(cmd, nil)
		if err == nil {
			t.Fatal("runStatus must refuse --format json rather than emit human text")
		}
		if !strings.Contains(err.Error(), "--format json") {
			t.Errorf("error = %v, want it to name the rejected format", err)
		}
		if out.String() != "" {
			t.Errorf("want no output when the format is refused, got:\n%s", out.String())
		}
	})
}
