package repository

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/mcp"
	"github.com/ferro-labs/ai-gateway/plugin"
)

// TestScrubArgs covers both forms a credential takes on a stdio command line —
// the element after its flag and the value joined to it — and the flag names
// and env references that must survive for the served config to stay usable.
func TestScrubArgs(t *testing.T) {
	// Built at runtime so the literal does not trip a credential scanner.
	secret := "sk-" + strings.Repeat("x", 24)

	cases := []struct {
		name string
		args []string
		want []string
	}{
		{name: "nil", args: nil, want: nil},
		{name: "separate flag value", args: []string{"--api-key", secret}, want: []string{"--api-key", redactedPlaceholder}},
		{name: "inline flag value", args: []string{"--api-key=" + secret}, want: []string{"--api-key=" + redactedPlaceholder}},
		{name: "short flag value", args: []string{"-k", secret}, want: []string{"-k", redactedPlaceholder}},
		{name: "valueless flags are kept", args: []string{"-y", "--verbose"}, want: []string{"-y", "--verbose"}},
		{name: "positional value", args: []string{"serve", "/data"}, want: []string{redactedPlaceholder, redactedPlaceholder}},
		{name: "env reference", args: []string{"--api-key", "${MCP_TOKEN}"}, want: []string{"--api-key", "${MCP_TOKEN}"}},
		{name: "inline env reference", args: []string{"--api-key=${MCP_TOKEN}"}, want: []string{"--api-key=${MCP_TOKEN}"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := scrubArgs(tc.args); !slices.Equal(got, tc.want) {
				t.Fatalf("scrubArgs(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

// TestScrubConfigSecrets_MCPArgs proves the whole path: the served copy is
// redacted, the live config the gateway runs on is untouched (ScrubConfigSecrets
// takes cfg by value, so a shared backing array would write the placeholder into
// a running server's command line), and a scrubbed config sent back is refused
// rather than persisted over the real credential.
func TestScrubConfigSecrets_MCPArgs(t *testing.T) {
	secret := "sk-" + strings.Repeat("x", 24)

	live := config.Config{
		MCPServers: []mcp.ServerConfig{{
			Name:    "search",
			Command: "npx",
			Args:    []string{"-y", "server-search", "--api-key", secret},
		}},
	}

	scrubbed := ScrubConfigSecrets(live)

	want := []string{"-y", redactedPlaceholder, "--api-key", redactedPlaceholder}
	if got := scrubbed.MCPServers[0].Args; !slices.Equal(got, want) {
		t.Fatalf("scrubbed args = %v, want %v", got, want)
	}
	if got := live.MCPServers[0].Args[3]; got != secret {
		t.Fatalf("live config mutated: args[3] = %q", got)
	}
	if got := RedactedSecretField(scrubbed); got != "mcp_servers[0].args" {
		t.Fatalf("RedactedSecretField = %q, want %q", got, "mcp_servers[0].args")
	}

	inline := config.Config{
		MCPServers: []mcp.ServerConfig{{
			Name:    "search",
			Command: "npx",
			Args:    []string{"--api-key=" + redactedPlaceholder},
		}},
	}
	if got := RedactedSecretField(inline); got != "mcp_servers[0].args" {
		t.Fatalf("RedactedSecretField on an inline value = %q, want %q", got, "mcp_servers[0].args")
	}
}

// TestShownKeys is the rule itself: which map fields have a declared schema, and
// what that schema admits.
//
// Every case that wants false is a key a name-matching rule had to recognise in
// advance to withhold. None of them has to be recognised now — they are withheld
// for not having been declared, which is the state of every key nobody thought
// of.
func TestShownKeys(t *testing.T) {
	cases := []struct {
		name  string
		owner any
		field string
		key   string
		want  bool
	}{
		// The one map the gateway consumes itself. Both halves of an entry are
		// model names, so there is no third party for a credential to reach.
		{name: "alias", owner: config.Config{}, field: "Aliases", key: "fast", want: true},

		// A plugin declares the keys it reads.
		{name: "declared setting", owner: config.PluginConfig{Name: "request-logger"}, field: "Config", key: "level", want: true},
		{name: "declared numeric setting", owner: config.PluginConfig{Name: "rate-limit"}, field: "Config", key: "key_rpm", want: true},
		{name: "another plugin's setting", owner: config.PluginConfig{Name: "request-logger"}, field: "Config", key: "burst"},
		{name: "undeclared key", owner: config.PluginConfig{Name: "budget"}, field: "Config", key: "pat"},
		{name: "undeclared key sid", owner: config.PluginConfig{Name: "budget"}, field: "Config", key: "sid"},
		{name: "undeclared key dsn", owner: config.PluginConfig{Name: "budget"}, field: "Config", key: "dsn"},
		{name: "plugin with no catalog entry", owner: config.PluginConfig{Name: "third-party"}, field: "Config", key: "level"},
		{name: "unnamed plugin", owner: config.PluginConfig{}, field: "Config", key: "level"},

		// Free-form maps handed to something outside the gateway. No schema, so
		// no key is a setting.
		{name: "mcp env", owner: mcp.ServerConfig{}, field: "Env", key: "LOG_LEVEL"},
		{name: "mcp headers", owner: mcp.ServerConfig{}, field: "Headers", key: "Accept"},
		{name: "exporter config", owner: config.ExporterConfig{}, field: "Config", key: "project"},
		{name: "tracing headers", owner: config.TracingConfig{}, field: "Headers", key: "x-scope-orgid"},

		// The policy is per field, not per struct.
		{name: "wrong field on Config", owner: config.Config{}, field: "Plugins", key: "fast"},
		{name: "wrong field on PluginConfig", owner: config.PluginConfig{Name: "budget"}, field: "Name", key: "store_id"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shownKeys(tc.owner, tc.field)(tc.key); got != tc.want {
				t.Fatalf("shownKeys(%T, %q)(%q) = %v, want %v", tc.owner, tc.field, tc.key, got, tc.want)
			}
		})
	}
}

// TestDeclaredSettings_MatchesCatalog holds the allowlist to its source: every
// key a built-in plugin declares is shown for that plugin and for no other, and
// the source is plugin.Builtins rather than a copy kept here.
func TestDeclaredSettings_MatchesCatalog(t *testing.T) {
	for _, entry := range plugin.Builtins() {
		t.Run(entry.Name, func(t *testing.T) {
			shown := declaredSettings(entry.Name)
			for _, key := range entry.Settings {
				if !shown(key) {
					t.Errorf("%s declares %q and it is withheld", entry.Name, key)
				}
			}
			if shown("not_a_declared_setting") {
				t.Errorf("%s shows a key it never declared", entry.Name)
			}
		})
	}
}

// TestScrubFreeFormMap covers the two halves of one map: a shown key keeps its
// shape and loses only its secret parts, a withheld key loses the whole value at
// any depth, and a ${VAR} reference survives either way.
func TestScrubFreeFormMap(t *testing.T) {
	openAIKey := "sk-" + strings.Repeat("y", 30)

	live := map[string]any{
		"level":     "debug",
		"webhook":   openAIKey,
		"envref":    "${LOG_LEVEL}",
		"pat":       "ghp_SYNTHETICpat0123456789abcdef",
		"threshold": 42,
		"nested":    map[string]any{"inner": "plain", "count": 3},
	}
	shown := func(key string) bool { return key == "level" || key == "webhook" || key == "envref" || key == "nested" }

	// scrubFreeFormMap replaces the variable with a new map, so hold the
	// original header separately to prove it was not written through.
	original := live
	v := reflect.ValueOf(&live).Elem()
	scrubFreeFormMap(v, shown)
	got := v.Interface().(map[string]any)

	if got["level"] != "debug" {
		t.Errorf("level = %v, want it readable", got["level"])
	}
	if s, _ := got["webhook"].(string); strings.Contains(s, openAIKey) {
		t.Errorf("a credential inlined under a declared key survived: %q", s)
	}
	if got["envref"] != "${LOG_LEVEL}" {
		t.Errorf("envref = %v, want the reference served as written", got["envref"])
	}
	if _, ok := got["pat"]; ok {
		t.Errorf("withheld key \"pat\" was served: %v", got)
	}
	if _, ok := got["threshold"]; ok {
		t.Errorf("withheld key \"threshold\" was served: %v", got)
	}
	// A non-string under a withheld key goes too: mapStrings copied it through
	// untouched, so a number was served as it stood.
	for key, value := range got {
		if !strings.Contains(key, redactedMarker) {
			continue
		}
		if _, isNumber := value.(int); isNumber {
			t.Errorf("a non-string value survived withholding: %v = %v", key, value)
		}
	}
	nested, ok := got["nested"].(map[string]any)
	if !ok {
		t.Fatalf("nested = %T, want map[string]any", got["nested"])
	}
	if nested["inner"] != "plain" || nested["count"] != 3 {
		t.Errorf("nested under a declared key = %v, want it readable", nested)
	}
	if original["pat"] == redactedPlaceholder {
		t.Error("the live map was mutated")
	}
}

// TestMapStrings_WalksEveryContainerType is the shape defect, stated as a test.
//
// The judgement a key makes has to cover every string beneath it. While the walk
// enumerated concrete types, it carried that judgement into map[string]any and
// descended past the map[string]string, []map[string]any, []map[string]string
// and map[string][]string beside it — so a container type nobody listed served
// its contents. Walking by kind removes the list, and with it the trap.
func TestMapStrings_WalksEveryContainerType(t *testing.T) {
	secret := strings.Repeat("q", 8) + "-literal-config-value"

	cases := []struct {
		name string
		in   any
	}{
		{name: "string", in: secret},
		{name: "map[string]any", in: map[string]any{"k": secret}},
		{name: "map[string]string", in: map[string]string{"k": secret}},
		{name: "[]any", in: []any{secret}},
		{name: "[]string", in: []string{secret}},
		{name: "[]map[string]any", in: []map[string]any{{"k": secret}}},
		{name: "[]map[string]string", in: []map[string]string{{"k": secret}}},
		{name: "map[string][]string", in: map[string][]string{"k": {secret}}},
		{name: "map[string]map[string]string", in: map[string]map[string]string{"o": {"k": secret}}},
		{name: "[][]string", in: [][]string{{secret}}},
		{name: "map[string]any holding []any of map", in: map[string]any{"k": []any{map[string]any{"i": secret}}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapStrings(reflect.ValueOf(tc.in), redactValue).Interface()
			if leaked := findString(reflect.ValueOf(got), secret); leaked {
				t.Fatalf("the literal survived in %T: %v", got, got)
			}
			if !findString(reflect.ValueOf(got), redactedPlaceholder) {
				t.Fatalf("no placeholder written into %T: %v", got, got)
			}
			if !findString(reflect.ValueOf(tc.in), secret) {
				t.Fatalf("the input was mutated: %v", tc.in)
			}
		})
	}
}

// TestMapStrings_PreservesEnvRefsAndNonStrings guards the two things the deep
// withhold must not touch.
func TestMapStrings_PreservesEnvRefsAndNonStrings(t *testing.T) {
	in := map[string]any{
		"ref":   "${SEARCH_TOKEN}",
		"count": 7,
		"flag":  true,
		"empty": "",
		"list":  []any{"${A}", 1.5, nil},
	}

	got := mapStrings(reflect.ValueOf(in), redactValue).Interface().(map[string]any)

	if got["ref"] != "${SEARCH_TOKEN}" {
		t.Errorf("ref = %v, want the reference preserved", got["ref"])
	}
	if got["count"] != 7 || got["flag"] != true {
		t.Errorf("non-strings were rewritten: %v", got)
	}
	if got["empty"] != "" {
		t.Errorf("empty = %q, want an empty string left alone", got["empty"])
	}
	list, ok := got["list"].([]any)
	if !ok || list[0] != "${A}" || list[1] != 1.5 || list[2] != nil {
		t.Errorf("list = %v, want references, numbers and nil preserved", got["list"])
	}
}

// findString reports whether want appears in any string anywhere inside v.
func findString(v reflect.Value, want string) bool {
	switch v.Kind() {
	case reflect.String:
		return strings.Contains(v.String(), want)
	case reflect.Interface, reflect.Pointer:
		return !v.IsNil() && findString(v.Elem(), want)
	case reflect.Map:
		for iter := v.MapRange(); iter.Next(); {
			if findString(iter.Value(), want) {
				return true
			}
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			if findString(v.Index(i), want) {
				return true
			}
		}
	}
	return false
}
