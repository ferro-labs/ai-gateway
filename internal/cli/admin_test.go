package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestToSlice(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   any
		want int
	}{
		{name: "json array of objects", in: []any{map[string]any{"a": 1}, map[string]any{"b": 2}}, want: 2},
		{name: "single object is wrapped", in: map[string]any{"a": 1}, want: 1},
		{name: "array with non-object entries skips them", in: []any{map[string]any{"a": 1}, "nope"}, want: 1},
		{name: "unsupported type yields nil", in: 42, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := len(toSlice(tt.in)); got != tt.want {
				t.Errorf("toSlice length = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestMapHelpers(t *testing.T) {
	t.Parallel()
	m := map[string]any{
		"name":    "openai",
		"revoked": true,
		"count":   float64(7),
		"nilval":  nil,
	}

	if got := str(m, "name"); got != "openai" {
		t.Errorf("str = %q, want openai", got)
	}
	if got := str(m, "missing"); got != "" {
		t.Errorf("str(missing) = %q, want empty", got)
	}
	if got := str(m, "nilval"); got != "" {
		t.Errorf("str(nil) = %q, want empty", got)
	}
	if got := strBool(m, "revoked"); got != "yes" {
		t.Errorf("strBool(true) = %q, want yes", got)
	}
	if got := strBool(m, "missing"); got != "no" {
		t.Errorf("strBool(missing) = %q, want no", got)
	}
	if got := numVal(m, "count"); got != 7 {
		t.Errorf("numVal = %v, want 7", got)
	}
	if got := numVal(m, "missing"); got != 0 {
		t.Errorf("numVal(missing) = %v, want 0", got)
	}
}

func TestFmtTime(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "rfc3339 renders short UTC", in: "2026-07-15T09:30:00Z", want: "2026-07-15 09:30 UTC"},
		{name: "empty renders dash", in: "", want: "-"},
		{name: "unparseable returns the raw value", in: "not-a-time", want: "not-a-time"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := map[string]any{"ts": tt.in}
			if got := fmtTime(m, "ts"); got != tt.want {
				t.Errorf("fmtTime = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestJSONSlice_Rendering(t *testing.T) {
	t.Parallel()
	js := &jsonSlice{
		headers: []string{"NAME"},
		data:    []map[string]any{{"name": "openai"}},
		rowFn:   func(m map[string]any) []string { return []string{str(m, "name")} },
	}

	if got := js.Headers(); len(got) != 1 || got[0] != "NAME" {
		t.Errorf("Headers = %v, want [NAME]", got)
	}
	rows := js.Rows()
	if len(rows) != 1 || rows[0][0] != "openai" {
		t.Errorf("Rows = %v, want [[openai]]", rows)
	}

	b, err := json.Marshal(js)
	if err != nil || !strings.Contains(string(b), "openai") {
		t.Errorf("MarshalJSON = %s, err = %v", b, err)
	}
	y, err := js.MarshalYAML()
	if err != nil {
		t.Errorf("MarshalYAML err = %v", err)
	}
	if _, ok := y.([]map[string]any); !ok {
		t.Errorf("MarshalYAML = %T, want []map[string]any", y)
	}
}

// ── Admin sub-command handlers (through cobra Execute) ──────────────────────────

func TestAdminKeysList(t *testing.T) {
	srv := stubGateway(t, map[string]http.HandlerFunc{
		"/admin/keys": jsonHandler(http.StatusOK, `[{"id":"k1","name":"prod","scope":"admin","revoked":false}]`),
	})

	out, err := execAdmin(t, srv.URL, "admin", "keys", "list")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	for _, want := range []string{"ID", "k1", "prod", "admin"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestAdminKeysGet(t *testing.T) {
	srv := stubGateway(t, map[string]http.HandlerFunc{
		"/admin/keys/k1": jsonHandler(http.StatusOK, `{"id":"k1","name":"prod"}`),
	})

	out, err := execAdmin(t, srv.URL, "admin", "keys", "get", "k1")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "k1") {
		t.Errorf("want key detail, got:\n%s", out)
	}
}

func TestAdminKeysCreate(t *testing.T) {
	srv := stubGateway(t, map[string]http.HandlerFunc{
		"/admin/keys": jsonHandler(http.StatusOK, `{"id":"new","key":"fgw_secret"}`),
	})

	t.Run("with a valid expiry", func(t *testing.T) {
		out, err := execAdmin(t, srv.URL, "admin", "keys", "create", "--name", "ci", "--expires-in", "1h")
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		if !strings.Contains(out, "new") {
			t.Errorf("want created key in output, got:\n%s", out)
		}
	})

	t.Run("rejects an invalid expiry duration", func(t *testing.T) {
		_, err := execAdmin(t, srv.URL, "admin", "keys", "create", "--expires-in", "bogus")
		if err == nil || !strings.Contains(err.Error(), "invalid --expires-in") {
			t.Fatalf("want expiry parse error, got %v", err)
		}
	})
}

func TestAdminKeysRevoke(t *testing.T) {
	srv := stubGateway(t, map[string]http.HandlerFunc{
		"/admin/keys/k1/revoke": jsonHandler(http.StatusOK, `{}`),
	})

	out, err := execAdmin(t, srv.URL, "admin", "keys", "revoke", "k1")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "Key revoked.") {
		t.Errorf("want success message, got:\n%s", out)
	}
}

func TestAdminLogsList(t *testing.T) {
	srv := stubGateway(t, map[string]http.HandlerFunc{
		"/admin/logs": jsonHandler(http.StatusOK, `[{"trace_id":"t1","provider":"openai","model":"gpt-4","status":200,"latency_ms":42}]`),
	})

	out, err := execAdmin(t, srv.URL, "admin", "logs", "list")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	for _, want := range []string{"TRACE_ID", "t1", "openai", "gpt-4"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestAdminConfigSet(t *testing.T) {
	// execAdmin resets the command flags before each run, so these subtests are
	// independent of ordering.
	t.Run("requires the file flag", func(t *testing.T) {
		_, err := execAdmin(t, "http://127.0.0.1:1", "admin", "config", "set")
		if err == nil || !strings.Contains(err.Error(), "--file is required") {
			t.Fatalf("want --file required error, got %v", err)
		}
	})

	t.Run("applies a JSON config file", func(t *testing.T) {
		srv := stubGateway(t, map[string]http.HandlerFunc{
			"/admin/config": jsonHandler(http.StatusOK, `{}`),
		})
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(`{"strategy":{"mode":"fallback"},"targets":[{"virtual_key":"openai"}]}`), 0600); err != nil {
			t.Fatalf("write config: %v", err)
		}

		out, err := execAdmin(t, srv.URL, "admin", "config", "set", "--file", path)
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		if !strings.Contains(out, "Configuration updated.") {
			t.Errorf("want success message, got:\n%s", out)
		}
	})
}
