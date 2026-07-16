package cli

import (
	"net/http"
	"strings"
	"testing"
)

func TestRunPlugins(t *testing.T) {
	const listBody = `[{"name":"word-filter","type":"guardrail","enabled":true}]`

	t.Run("table renders registered plugins", func(t *testing.T) {
		srv := stubGateway(t, map[string]http.HandlerFunc{
			"/admin/plugins": jsonHandler(http.StatusOK, listBody),
		})
		cmd, out := newHandlerCmd(t, srv.URL, "table")

		if err := runPlugins(cmd, nil); err != nil {
			t.Fatalf("runPlugins: %v", err)
		}
		got := out.String()
		for _, want := range []string{"NAME", "ENABLED", "word-filter", "guardrail"} {
			if !strings.Contains(got, want) {
				t.Errorf("output missing %q:\n%s", want, got)
			}
		}
	})

	t.Run("json format bypasses the table", func(t *testing.T) {
		srv := stubGateway(t, map[string]http.HandlerFunc{
			"/admin/plugins": jsonHandler(http.StatusOK, listBody),
		})
		cmd, out := newHandlerCmd(t, srv.URL, "json")

		if err := runPlugins(cmd, nil); err != nil {
			t.Fatalf("runPlugins: %v", err)
		}
		got := out.String()
		if !strings.Contains(got, `"name"`) || !strings.Contains(got, "word-filter") {
			t.Errorf("want JSON payload, got:\n%s", got)
		}
	})

	t.Run("empty list prints a friendly message", func(t *testing.T) {
		srv := stubGateway(t, map[string]http.HandlerFunc{
			"/admin/plugins": jsonHandler(http.StatusOK, `[]`),
		})
		cmd, out := newHandlerCmd(t, srv.URL, "table")

		if err := runPlugins(cmd, nil); err != nil {
			t.Fatalf("runPlugins: %v", err)
		}
		if !strings.Contains(out.String(), "No plugins registered.") {
			t.Errorf("want empty-state message, got:\n%s", out.String())
		}
	})

	t.Run("propagates an admin API error", func(t *testing.T) {
		srv := stubGateway(t, map[string]http.HandlerFunc{
			"/admin/plugins": jsonHandler(http.StatusInternalServerError, `{"error":{"message":"boom"}}`),
		})
		cmd, _ := newHandlerCmd(t, srv.URL, "table")

		err := runPlugins(cmd, nil)
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("want error carrying 'boom', got %v", err)
		}
	})
}
