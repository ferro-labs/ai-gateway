package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
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

func TestEnvelopeRows(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   any
		want int
	}{
		{name: "data envelope yields its rows", in: map[string]any{
			"data":    []any{map[string]any{"a": 1}, map[string]any{"b": 2}},
			"summary": map[string]any{"total_entries": 2},
		}, want: 2},
		{name: "bare array passes through", in: []any{map[string]any{"a": 1}}, want: 1},
		{name: "object without data is one row", in: map[string]any{"a": 1}, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := len(toSlice(envelopeRows(tt.in))); got != tt.want {
				t.Errorf("row count = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestStrList(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   any
		want string
	}{
		{name: "single element", in: []any{"read_only"}, want: "read_only"},
		{name: "multiple elements join on comma", in: []any{"admin", "read_only"}, want: "admin,read_only"},
		{name: "empty array", in: []any{}, want: ""},
		{name: "absent field", in: nil, want: ""},
		{name: "non-array field", in: "admin", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := strList(map[string]any{"scopes": tt.in}, "scopes"); got != tt.want {
				t.Errorf("strList = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFmtNum(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   any
		want string
	}{
		{name: "number rounds to whole units", in: float64(42.6), want: "43"},
		{name: "zero is a measurement", in: float64(0), want: "0"},
		{name: "null renders a dash", in: nil, want: "-"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := fmtNum(map[string]any{"duration_ms": tt.in}, "duration_ms"); got != tt.want {
				t.Errorf("fmtNum = %q, want %q", got, tt.want)
			}
		})
	}
	if got := fmtNum(map[string]any{}, "duration_ms"); got != "-" {
		t.Errorf("fmtNum(absent) = %q, want -", got)
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

// ── Admin sub-command handlers (called directly on a fresh command) ─────────────

// Payloads below mirror what the Admin API actually serializes: model.APIKey
// carries `scopes` as an array, and the paginated endpoints wrap their rows in
// a "data" envelope.

func TestRunKeysList(t *testing.T) {
	srv := stubGateway(t, map[string]http.HandlerFunc{
		"/admin/keys": jsonHandler(http.StatusOK, `[{"id":"k1","name":"prod","scopes":["admin","read_only"],"revoked":false}]`),
	})
	cmd, out := newHandlerCmd(t, srv.URL, "table")

	if err := runKeysList(cmd, nil); err != nil {
		t.Fatalf("runKeysList: %v", err)
	}
	for _, want := range []string{"SCOPES", "k1", "prod", "admin,read_only"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunKeysGet(t *testing.T) {
	srv := stubGateway(t, map[string]http.HandlerFunc{
		"/admin/keys/k1": jsonHandler(http.StatusOK, `{"id":"k1","name":"prod"}`),
	})
	cmd, out := newHandlerCmd(t, srv.URL, "table")

	if err := runKeysGet(cmd, []string{"k1"}); err != nil {
		t.Fatalf("runKeysGet: %v", err)
	}
	if !strings.Contains(out.String(), "k1") {
		t.Errorf("want key detail, got:\n%s", out.String())
	}
}

// TestRunKeysCreate_ScopesWireFormat pins the created key to the scope the
// operator asked for. The Admin API grants admin to a create request that
// carries no scope list, so a body whose scope never reaches `scopes` mints an
// admin key for every invocation, including the default one.
func TestRunKeysCreate_ScopesWireFormat(t *testing.T) {
	scopeFlagDefault := keysCreateCmd.Flags().Lookup("scope").DefValue

	tests := []struct {
		name  string
		scope string
		want  []string
	}{
		{name: "read only", scope: "read_only", want: []string{"read_only"}},
		{name: "admin", scope: "admin", want: []string{"admin"}},
		{name: "flag default", scope: scopeFlagDefault, want: []string{"read_only"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sent struct {
				Name   string   `json:"name"`
				Scopes []string `json:"scopes"`
			}
			srv := stubGateway(t, map[string]http.HandlerFunc{
				"/admin/keys": func(w http.ResponseWriter, r *http.Request) {
					if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
						t.Errorf("decode request body: %v", err)
					}
					jsonHandler(http.StatusOK, `{"id":"new","key":"fgw_secret"}`)(w, r)
				},
			})
			cmd, _ := newHandlerCmd(t, srv.URL, "table")
			cmd.Flags().String("name", "ci", "")
			cmd.Flags().String("scope", tt.scope, "")
			cmd.Flags().String("expires-in", "", "")

			if err := runKeysCreate(cmd, nil); err != nil {
				t.Fatalf("runKeysCreate: %v", err)
			}
			if !slices.Equal(sent.Scopes, tt.want) {
				t.Errorf("scopes = %v, want %v", sent.Scopes, tt.want)
			}
		})
	}
}

func TestRunKeysCreate(t *testing.T) {
	srv := stubGateway(t, map[string]http.HandlerFunc{
		"/admin/keys": jsonHandler(http.StatusOK, `{"id":"new","key":"fgw_secret"}`),
	})

	t.Run("with a valid expiry", func(t *testing.T) {
		cmd, out := newHandlerCmd(t, srv.URL, "table")
		cmd.Flags().String("name", "ci", "")
		cmd.Flags().String("scope", "read_only", "")
		cmd.Flags().String("expires-in", "1h", "")

		if err := runKeysCreate(cmd, nil); err != nil {
			t.Fatalf("runKeysCreate: %v", err)
		}
		if !strings.Contains(out.String(), "new") {
			t.Errorf("want created key in output, got:\n%s", out.String())
		}
	})

	t.Run("rejects an invalid expiry duration", func(t *testing.T) {
		cmd, _ := newHandlerCmd(t, srv.URL, "table")
		cmd.Flags().String("name", "", "")
		cmd.Flags().String("scope", "read_only", "")
		cmd.Flags().String("expires-in", "bogus", "")

		err := runKeysCreate(cmd, nil)
		if err == nil || !strings.Contains(err.Error(), "invalid --expires-in") {
			t.Fatalf("want expiry parse error, got %v", err)
		}
	})
}

func TestRunKeysRevoke(t *testing.T) {
	srv := stubGateway(t, map[string]http.HandlerFunc{
		"/admin/keys/k1/revoke": jsonHandler(http.StatusOK, `{}`),
	})
	cmd, out := newHandlerCmd(t, srv.URL, "table")

	if err := runKeysRevoke(cmd, []string{"k1"}); err != nil {
		t.Fatalf("runKeysRevoke: %v", err)
	}
	if !strings.Contains(out.String(), "Key revoked.") {
		t.Errorf("want success message, got:\n%s", out.String())
	}
}

func TestRunConfigHistory(t *testing.T) {
	srv := stubGateway(t, map[string]http.HandlerFunc{
		"/admin/config/history": jsonHandler(http.StatusOK,
			`{"data":[{"version":3,"updated_at":"2026-07-15T09:30:00Z","rolled_back_from":1}],"summary":{"total_versions":1}}`),
	})
	cmd, out := newHandlerCmd(t, srv.URL, "table")

	if err := runConfigHistory(cmd, nil); err != nil {
		t.Fatalf("runConfigHistory: %v", err)
	}
	for _, want := range []string{"VERSION", "3", "2026-07-15 09:30 UTC", "1"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunConfigRollback(t *testing.T) {
	srv := stubGateway(t, map[string]http.HandlerFunc{
		"/admin/config/rollback/2": jsonHandler(http.StatusOK, `{}`),
	})
	cmd, out := newHandlerCmd(t, srv.URL, "table")

	if err := runConfigRollback(cmd, []string{"2"}); err != nil {
		t.Fatalf("runConfigRollback: %v", err)
	}
	if !strings.Contains(out.String(), "Rolled back to version 2.") {
		t.Errorf("want rollback message, got:\n%s", out.String())
	}
}

func TestRunConfigSet(t *testing.T) {
	// Each subtest builds its own command, so there is no shared flag state and
	// no dependence on execution order.
	t.Run("requires the file flag", func(t *testing.T) {
		cmd, _ := newHandlerCmd(t, "http://127.0.0.1:1", "table")
		cmd.Flags().String("file", "", "")

		err := runConfigSet(cmd, nil)
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
		cmd, out := newHandlerCmd(t, srv.URL, "table")
		cmd.Flags().String("file", path, "")

		if err := runConfigSet(cmd, nil); err != nil {
			t.Fatalf("runConfigSet: %v", err)
		}
		if !strings.Contains(out.String(), "Configuration updated.") {
			t.Errorf("want success message, got:\n%s", out.String())
		}
	})
}

func TestRunLogsList(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "measured entry",
			body: `{"data":[{"trace_id":"t1","provider":"openai","model":"gpt-4","stage":"after_request",` +
				`"duration_ms":42,"created_at":"2026-07-15T09:30:00Z"}],"summary":{"total_entries":1,"returned_entries":1}}`,
			want: []string{"TRACE_ID", "t1", "openai", "gpt-4", "after_request", "42", "2026-07-15 09:30 UTC"},
		},
		{
			name: "unmeasured duration renders a dash",
			body: `{"data":[{"trace_id":"t2","provider":"anthropic","model":"claude","stage":"on_error",` +
				`"duration_ms":null,"created_at":"2026-07-15T09:31:00Z"}],"summary":{"total_entries":1,"returned_entries":1}}`,
			want: []string{"t2", "anthropic", "on_error", "2026-07-15 09:31 UTC"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := stubGateway(t, map[string]http.HandlerFunc{
				"/admin/logs": jsonHandler(http.StatusOK, tt.body),
			})
			cmd, out := newHandlerCmd(t, srv.URL, "table")
			cmd.Flags().Int("limit", 50, "")

			if err := runLogsList(cmd, nil); err != nil {
				t.Fatalf("runLogsList: %v", err)
			}
			for _, want := range tt.want {
				if !strings.Contains(out.String(), want) {
					t.Errorf("output missing %q:\n%s", want, out.String())
				}
			}
		})
	}
}

func TestRunProvidersList(t *testing.T) {
	srv := stubGateway(t, map[string]http.HandlerFunc{
		"/admin/providers": jsonHandler(http.StatusOK,
			`[{"name":"openai","models":[{"id":"gpt-4"},{"id":"gpt-4o"}]},{"name":"groq","models":[]}]`),
	})
	cmd, out := newHandlerCmd(t, srv.URL, "table")

	if err := runProvidersList(cmd, nil); err != nil {
		t.Fatalf("runProvidersList: %v", err)
	}
	for _, want := range []string{"PROVIDER", "openai", "2", "groq", "0"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}
