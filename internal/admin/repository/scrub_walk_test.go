package repository

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/mcp"
	"github.com/ferro-labs/ai-gateway/plugin"
)

// TestScrubConfigSecrets_MCPServerURL is F56.
//
// GET /admin/config served mcp_servers[].url verbatim, access token and all,
// next to a properly redacted Authorization header — the redacted header beside
// the leaked URL is what shows the intent was there and the field list had
// simply been missed. A read-only key was enough to read it.
func TestScrubConfigSecrets_MCPServerURL(t *testing.T) {
	live := config.Config{
		MCPServers: []mcp.ServerConfig{{
			Name:    "search",
			URL:     "https://mcp.example.com/mcp?access_token=SECRET-IN-URL-12345&mode=stream",
			Headers: map[string]string{"Authorization": "Bearer SECRET-IN-HEADER-67890"},
		}},
	}

	got := ScrubConfigSecrets(live).MCPServers[0]

	if strings.Contains(got.URL, "SECRET-IN-URL-12345") {
		t.Fatalf("access token survived scrubbing: %q", got.URL)
	}
	// The URL stays identifiable — an operator must still be able to tell which
	// server is configured — and the non-secret host and path are untouched.
	if !strings.Contains(got.URL, "mcp.example.com/mcp") {
		t.Fatalf("host and path should survive, got %q", got.URL)
	}
	if !strings.Contains(got.URL, redactedPlaceholder) {
		t.Fatalf("expected the placeholder in the query, got %q", got.URL)
	}
	if _, ok := got.Headers["Authorization"]; ok {
		t.Fatalf("withheld header name was served: %v", got.Headers)
	}
	if len(got.Headers) != 1 {
		t.Fatalf("%d headers, want one withheld placeholder: %v", len(got.Headers), got.Headers)
	}
	if live.MCPServers[0].URL != "https://mcp.example.com/mcp?access_token=SECRET-IN-URL-12345&mode=stream" {
		t.Fatalf("live config mutated: %q", live.MCPServers[0].URL)
	}
	// Whatever is scrubbed on the way out is refused on the way back in.
	if field := RedactedSecretField(ScrubConfigSecrets(live)); field != "mcp_servers[0].url" {
		t.Fatalf("RedactedSecretField = %q, want mcp_servers[0].url", field)
	}
}

// TestScrubConfigSecrets_TracingEndpoint covers the other field the
// hand-maintained list missed. A collector endpoint routinely carries basic-auth
// credentials in its userinfo.
func TestScrubConfigSecrets_TracingEndpoint(t *testing.T) {
	// Assembled at runtime: a password-in-URL literal is what a credential
	// scanner is built to flag, and this is a fixture rather than a credential.
	endpoint := "https://otel:" + strings.Repeat("p", 12) + "@collector.example.com:4318"
	live := config.Config{Observability: config.ObservabilityConfig{
		Tracing: config.TracingConfig{Endpoint: endpoint},
	}}

	got := ScrubConfigSecrets(live).Observability.Tracing.Endpoint
	if strings.Contains(got, strings.Repeat("p", 12)) {
		t.Fatalf("collector password survived: %q", got)
	}
	if !strings.Contains(got, "collector.example.com:4318") {
		t.Fatalf("host should survive, got %q", got)
	}
}

// TestScrubConfigSecrets_KeepsRoutingReadable is the counterweight. The walk
// reaches every string field, so it has to leave the ones that are settings
// rather than secrets intact — otherwise the served config says nothing.
func TestScrubConfigSecrets_KeepsRoutingReadable(t *testing.T) {
	live := config.Config{
		APIVersion: "v1",
		Strategy:   config.StrategyConfig{Mode: config.ModeFallback},
		Targets:    []config.Target{{VirtualKey: "openai"}, {VirtualKey: "mistral"}},
		Aliases:    map[string]string{"fast": "gpt-4o-mini"},
		Plugins: []config.PluginConfig{{
			Name:   "word-filter",
			Config: map[string]any{"blocked_words": []any{"password"}, "case_sensitive": false},
		}},
		MCPServers: []mcp.ServerConfig{{
			Name:         "files",
			Command:      "npx",
			AllowedTools: []string{"read_file", "list_dir"},
			URL:          "https://mcp.example.com/mcp",
		}},
	}

	got := ScrubConfigSecrets(live)

	if got.APIVersion != "v1" || got.Strategy.Mode != config.ModeFallback {
		t.Fatalf("routing settings were redacted: %+v", got.Strategy)
	}
	if got.Targets[0].VirtualKey != "openai" || got.Targets[1].VirtualKey != "mistral" {
		t.Fatalf("virtual keys were redacted: %+v", got.Targets)
	}
	if got.MCPServers[0].Name != "files" || got.MCPServers[0].Command != "npx" {
		t.Fatalf("server identity was redacted: %+v", got.MCPServers[0])
	}
	if got.MCPServers[0].AllowedTools[0] != "read_file" {
		t.Fatalf("allowed_tools were redacted: %v", got.MCPServers[0].AllowedTools)
	}
	if got.MCPServers[0].URL != "https://mcp.example.com/mcp" {
		t.Fatalf("credential-free URL was rewritten: %q", got.MCPServers[0].URL)
	}
	// A model alias and a guardrail's word list are settings the gateway itself
	// knows the meaning of: an operator who cannot read them back edits the
	// config by retyping it.
	if got.Aliases["fast"] != "gpt-4o-mini" {
		t.Fatalf("model alias was redacted, got %q", got.Aliases["fast"])
	}
	words, ok := got.Plugins[0].Config["blocked_words"].([]any)
	if !ok || len(words) != 1 || words[0] != "password" {
		t.Fatalf("blocked_words were redacted: %v", got.Plugins[0].Config["blocked_words"])
	}
	if got.Plugins[0].Config["case_sensitive"] != false {
		t.Fatalf("case_sensitive was rewritten: %v", got.Plugins[0].Config["case_sensitive"])
	}
	// Nothing here is a secret, so nothing is refused on the way back in.
	if field := RedactedSecretField(got); field != "" {
		t.Fatalf("RedactedSecretField = %q, want no redacted field", field)
	}
}

// TestScrubConfigSecrets_DeclaredPluginSettings is the readability half of the
// allowlist, driven from the catalog itself: every key a built-in plugin
// declares comes back readable, whatever its name looks like.
//
// Several of these — key_rpm, max_keys, max_tokens — were withheld by nothing
// but the accident of being numeric under the name-matching rule that preceded
// this one. They are readable on their merits now: their plugin declares them.
func TestScrubConfigSecrets_DeclaredPluginSettings(t *testing.T) {
	for _, entry := range plugin.Builtins() {
		t.Run(entry.Name, func(t *testing.T) {
			cfg := make(map[string]any, len(entry.Settings))
			for i, key := range entry.Settings {
				cfg[key] = "setting-value-" + strconv.Itoa(i)
			}
			live := config.Config{Plugins: []config.PluginConfig{{Name: entry.Name, Config: cfg}}}

			got := ScrubConfigSecrets(live).Plugins[0].Config
			for i, key := range entry.Settings {
				if want := "setting-value-" + strconv.Itoa(i); got[key] != want {
					t.Errorf("%s.%s = %v, want %q", entry.Name, key, got[key], want)
				}
			}
			if field := RedactedSecretField(ScrubConfigSecrets(live)); field != "" {
				t.Errorf("RedactedSecretField = %q, want a declared settings block served whole", field)
			}
		})
	}
}

// TestScrubConfigSecrets_UndeclaredKeysAreWithheld is the security half, and the
// reproduction that motivated the redesign.
//
// Every key here named a credential and matched no fragment of the vocabulary
// that used to decide this, so every one was served in plaintext to any caller
// holding a read-only key. None of them is recognised now either — they are
// withheld for not having been declared, which is what a name nobody enumerated
// gets by default.
func TestScrubConfigSecrets_UndeclaredKeysAreWithheld(t *testing.T) {
	planted := map[string]any{
		"pat":               "ghp_SYNTHETICpat0123456789abcdef",
		"sid":               "ACSYNTHETIC0123456789abcdef01234",
		"jwt":               "header.payload.signature",
		"hmac":              "3f9a1c",
		"otp":               "482913",
		"contraseña":        "clave-secreta",
		"dsn":               "host=db user=gw password=hunter2 dbname=gw",
		"account_sid":       "ACSYNTHETIC0123456789abcdef01234",
		"connection_string": "Server=db;User Id=gw;Password=hunter2",
		"nonce":             "9d8c7b6a",
		"seed":              "correct horse battery staple",
	}

	// Every free-form surface, so no one of them can be fixed in isolation.
	live := config.Config{
		Plugins:    []config.PluginConfig{{Name: "budget", Config: cloneAny(planted)}},
		MCPServers: []mcp.ServerConfig{{Name: "search", Headers: cloneString(planted), Env: cloneString(planted)}},
		Observability: config.ObservabilityConfig{
			Tracing:   config.TracingConfig{Headers: cloneString(planted)},
			Exporters: []config.ExporterConfig{{Name: "langsmith", Config: cloneAny(planted)}},
		},
	}

	got := ScrubConfigSecrets(live)

	// Asserted over the SERIALIZED form, which is what a caller actually
	// receives, rather than by reading each key back. Withholding now removes
	// the key as well as the value, so a per-key lookup would report nil and
	// pass whether or not the secret leaked somewhere else in the document.
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal scrubbed config: %v", err)
	}
	body := string(encoded)

	for key, secret := range planted {
		t.Run("value/"+key, func(t *testing.T) {
			if strings.Contains(body, secret.(string)) {
				t.Fatalf("planted secret for %q survived into the response", key)
			}
		})
		t.Run("key/"+key, func(t *testing.T) {
			// The name goes too: a credential can be inlined in either position,
			// and an env or header NAME is deployment topology in its own right.
			if strings.Contains(body, `"`+key+`"`) {
				t.Fatalf("withheld key %q was served", key)
			}
		})
	}

	// Withholding must not silently drop the entries: the count survives so an
	// operator can still see that settings exist.
	for path, n := range map[string]int{
		"plugins[0].config":                 len(got.Plugins[0].Config),
		"mcp_servers[0].headers":            len(got.MCPServers[0].Headers),
		"mcp_servers[0].env":                len(got.MCPServers[0].Env),
		"observability.tracing.headers":     len(got.Observability.Tracing.Headers),
		"observability.exporters[0].config": len(got.Observability.Exporters[0].Config),
	} {
		if n != len(planted) {
			t.Errorf("%s has %d entries, want %d withheld placeholders", path, n, len(planted))
		}
	}

	// A plugin the build does not ship declares nothing, so nothing under it is
	// shown — including a key another plugin happens to declare.
	third := ScrubConfigSecrets(config.Config{Plugins: []config.PluginConfig{{
		Name:   "third-party-guardrail",
		Config: map[string]any{"level": "debug", "pat": "ghp_SYNTHETICpat0123456789abcdef"},
	}}}).Plugins[0].Config
	if _, ok := third["level"]; ok {
		t.Errorf("a plugin with no catalog entry had a key served: %v", third)
	}
	if _, ok := third["pat"]; ok {
		t.Errorf("a plugin with no catalog entry had a key served: %v", third)
	}
	for key, value := range third {
		if !strings.Contains(key, redactedMarker) || value != redactedPlaceholder {
			t.Errorf("third-party config entry %q = %v, want both halves withheld", key, value)
		}
	}
}

func cloneAny(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneString(m map[string]any) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v.(string)
	}
	return out
}

// TestScrubConfigSecrets_MapCredentials is the other half of the counterweight:
// a declared setting is still run through the value rules, so an operator who
// inlined a real credential under one is covered.
func TestScrubConfigSecrets_MapCredentials(t *testing.T) {
	openAIKey := "sk-" + strings.Repeat("y", 30)
	opaque := strings.Repeat("x", 8) + "-literal-config-value"

	live := config.Config{
		Plugins: []config.PluginConfig{{
			Name: "budget",
			// store_id is declared, so it is shown — but a provider key pasted
			// into it goes on its shape alone. api_key is not declared by any
			// plugin, so it goes whatever it looks like.
			Config: map[string]any{"api_key": opaque, "store_id": openAIKey},
		}},
		MCPServers: []mcp.ServerConfig{{
			Name:    "search",
			Headers: map[string]string{"Authorization": opaque, "Accept": "application/json"},
			Env:     map[string]string{"SEARCH_TOKEN": opaque, "HOME": "/data"},
		}},
	}

	got := ScrubConfigSecrets(live)

	pluginCfg := got.Plugins[0].Config
	if _, ok := pluginCfg["api_key"]; ok {
		t.Errorf("withheld key \"api_key\" was served: %v", pluginCfg)
	}
	if v, _ := pluginCfg["store_id"].(string); strings.Contains(v, openAIKey) {
		t.Errorf("an inlined provider key survived under a declared setting: %q", v)
	}

	// An MCP server's headers and env carry transport credentials by nature and
	// declare no schema, so both keys go — an operator reads the server's
	// identity from name, command and url, which are named fields.
	server := got.MCPServers[0]
	for path, entries := range map[string]map[string]string{
		"headers": server.Headers,
		"env":     server.Env,
	} {
		for _, name := range []string{"Authorization", "Accept", "SEARCH_TOKEN", "HOME"} {
			if _, ok := entries[name]; ok {
				t.Errorf("%s served the withheld name %q: %v", path, name, entries)
			}
		}
		for key, value := range entries {
			if !strings.Contains(key, redactedMarker) {
				t.Errorf("%s key %q was not withheld", path, key)
			}
			if value != redactedPlaceholder && !isEnvRef(value) {
				t.Errorf("%s value under %q = %q, want the placeholder", path, key, value)
			}
		}
	}

	// Whatever is redacted on the way out is refused on the way back in,
	// including the partially-redacted value under the declared setting.
	if field := RedactedSecretField(got); field == "" {
		t.Error("RedactedSecretField found nothing to refuse")
	}
	onlyPartial := config.Config{Plugins: []config.PluginConfig{{
		Name:   "budget",
		Config: map[string]any{"store_id": ScrubConfigSecrets(live).Plugins[0].Config["store_id"]},
	}}}
	if field := RedactedSecretField(onlyPartial); field != "plugins[0].config" {
		t.Errorf("RedactedSecretField = %q, want plugins[0].config", field)
	}
}

// TestScrubConfigSecrets_EnvRefsSurviveWithholding pins the one thing that
// survives a withheld entry: the ${VAR} reference in its VALUE.
//
// The key does not — withholding covers it — so the reference is no longer
// attributable to the setting it configures. What it still does is keep
// warnStoredLiterals honest: that warning decides whether an operator inlined a
// credential by asking what scrubbing changed, so replacing references here
// would make it fire on every config that took the advice the warning gives.
func TestScrubConfigSecrets_EnvRefsSurviveWithholding(t *testing.T) {
	live := config.Config{
		MCPServers: []mcp.ServerConfig{{
			Name:    "search",
			Headers: map[string]string{"Authorization": "${SEARCH_TOKEN}"},
			//nolint:gosec // G101: an unresolved ${VAR} reference in a fixture, not a credential
			Env: map[string]string{"TOKEN": "${SOME_TOKEN}"},
		}},
		Observability: config.ObservabilityConfig{Exporters: []config.ExporterConfig{
			{Name: "langsmith", Config: map[string]any{"api_key": "${LANGSMITH_API_KEY}", "nested": map[string]any{"k": "${DEEP}"}}},
		}},
	}

	got := ScrubConfigSecrets(live)

	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(encoded)

	for _, ref := range []string{"${SEARCH_TOKEN}", "${SOME_TOKEN}", "${LANGSMITH_API_KEY}", "${DEEP}"} {
		if !strings.Contains(body, ref) {
			t.Errorf("reference %s was dropped: %s", ref, body)
		}
	}
	// The names they sat under are gone regardless.
	for _, name := range []string{`"Authorization"`, `"TOKEN"`, `"api_key"`, `"nested"`} {
		if strings.Contains(body, name) {
			t.Errorf("withheld key %s was served: %s", name, body)
		}
	}

	// And the config still round-trips: a body carrying only references is not
	// refused on the way back in.
	if field := RedactedLiteralField(got); field != "" {
		t.Errorf("a references-only config was refused at %q", field)
	}
}

// TestScrubConfigSecrets_LiteralKeyInAnyScalar proves the walk covers fields
// nobody enumerated: a credential pasted into any string field is removed by
// internal/redact rather than by this file knowing the field exists.
func TestScrubConfigSecrets_LiteralKeyInAnyScalar(t *testing.T) {
	key := "sk-" + strings.Repeat("x", 30)
	live := config.Config{MCPServers: []mcp.ServerConfig{{
		Name:    "search",
		Command: "run --api-key=" + key,
	}}}

	got := ScrubConfigSecrets(live).MCPServers[0].Command
	if strings.Contains(got, key) {
		t.Fatalf("credential survived in a field with no bespoke rule: %q", got)
	}
}

// TestScrubURLSecrets covers the URL rule directly, including the shapes that
// must be left alone.
func TestScrubURLSecrets(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"no url", "npx", "npx"},
		{"plain url", "https://api.example.com/v1", "https://api.example.com/v1"},
		{"query value", "https://h/p?token=abc", "https://h/p?token=" + redactedPlaceholder},
		{
			"every query value, host and keys kept",
			"https://h/p?a=1&b=2",
			"https://h/p?a=" + redactedPlaceholder + "&b=" + redactedPlaceholder,
		},
		{"env ref in query is preserved", "https://h/p?token=${T}", "https://h/p?token=${T}"},
		{"userinfo password", "https://u:pw@h:4318", "https://u:" + redactedPlaceholder + "@h:4318"},
		{"username alone is not a secret", "https://u@h:4318", "https://u@h:4318"},
		{"not a url", "just some text with :// in it", "just some text with :// in it"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := scrubURLSecrets(tc.in); got != tc.want {
				t.Fatalf("scrubURLSecrets(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
