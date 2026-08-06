package plugin

// BuiltinPlugin describes one plugin that ships with the gateway.
//
// It exists so an operator-facing surface — the dashboard's "Built in, not
// configured" panel, `GET /admin/plugins/catalog` — can say what a plugin is
// for and which keys its `config` block accepts without holding a second copy
// of that knowledge. The dashboard used to carry its own copy and four of its
// six entries named a key no plugin reads, in the panel that tells an operator
// to add one of these to the configuration.
//
// Settings names keys, not their types or defaults. A plugin's config is a
// `map[string]any` it interprets itself, so there is no schema to publish and
// inventing one would be a second thing to keep true. The keys are the part an
// operator cannot guess and the part that silently does nothing when wrong:
// an unknown key is accepted without complaint, so `requests_per_minute: 1`
// leaves the 100/second default in place.
type BuiltinPlugin struct {
	// Name is the name the factory is registered under and the name a
	// `plugins[].name` entry must use.
	Name string `json:"name"`
	// Type is what the plugin reports from Type(), which is what the gateway
	// acts on — not the `plugins[].type` written in the configuration, which it
	// does not consult.
	Type PluginType `json:"type"`
	// Summary is one line on what the plugin does, in operator terms.
	Summary string `json:"summary"`
	// Settings are the `config` keys the plugin reads, in the order they are
	// usually set. Keys the plugin accepts only to warn that they moved
	// elsewhere are omitted: advertising them would invite their use.
	Settings []string `json:"settings"`
	// FailsOpen reports whether a request survives this plugin breaking, which
	// is decided by Type alone and is filled in by Builtins rather than written
	// out per entry. It is the difference between a broken plugin costing a
	// line in the log and a broken plugin returning 500 to every caller.
	FailsOpen bool `json:"fails_open"`
}

// builtins describes the plugins in plugin/*.
//
// A central list rather than a method on Plugin: a plugin's config keys are
// read straight out of the map by Init, so there is nothing to reflect over,
// and requiring every plugin — including the ones built outside this
// repository — to implement a describe method would be a schema exercise to
// publish six lines of text.
//
// It cannot drift instead because catalog_test.go proves each field against
// the plugin itself: the set of Settings must equal the set of keys the
// package actually reads out of its config map, Type must equal what the
// plugin reports, and every registered plugin must appear here. A seventh
// plugin added with no entry, or an entry naming a key its Init does not read,
// fails the build.
var builtins = []BuiltinPlugin{
	{
		Name:     "word-filter",
		Type:     TypeGuardrail,
		Summary:  "Rejects a request whose text contains a blocked entry as a substring, not only as a whole word; screens the response when listed at after_request.",
		Settings: []string{"blocked_words", "case_sensitive"},
	},
	{
		Name:     "max-token",
		Type:     TypeGuardrail,
		Summary:  "Rejects a request that declares a completion ceiling above max_tokens, or exceeds the message-count or input-length limit; it never imposes a ceiling, so a request declaring none is uncapped.",
		Settings: []string{"max_tokens", "max_messages", "max_input_length"},
	},
	{
		Name:     "rate-limit",
		Type:     TypeRateLimit,
		Summary:  "Bounds request rate globally and per API key or user, independently of the per-IP HTTP limiter.",
		Settings: []string{"requests_per_second", "burst", "key_rpm", "user_rpm"},
	},
	{
		Name:     "budget",
		Type:     TypeRateLimit,
		Summary:  "Tracks estimated spend per API key and refuses requests once the configured budget is exhausted.",
		Settings: []string{"spend_limit_usd", "input_per_m_tokens", "output_per_m_tokens", "cache_read_per_m_tokens", "cache_write_per_m_tokens", "max_keys", "store_id"},
	},
	{
		Name:     "response-cache",
		Type:     TypeTransform,
		Summary:  "Serves an identical request from memory instead of calling a provider again, scoped to the API key that primed it — one credential's response is never served to another.",
		Settings: []string{"max_age", "max_entries"},
	},
	{
		Name:     "request-logger",
		Type:     TypeLogging,
		Summary:  "Records each request for the Request Logs page, and persists it when a request-log store is configured.",
		Settings: []string{"level", "persist"},
	},
}

// Builtins returns the catalog of plugins shipped with the gateway.
//
// FailsOpen is filled in here from the same predicate the plugin manager
// applies at runtime, so the answer served to an operator cannot disagree with
// the one the request path takes.
func Builtins() []BuiltinPlugin {
	catalog := make([]BuiltinPlugin, len(builtins))
	for i, entry := range builtins {
		entry.FailsOpen = errorFailsOpen(entry.Type)
		// Copied so a caller mutating the slice it was handed cannot reach the
		// package-level entry every later caller reads.
		entry.Settings = append([]string(nil), entry.Settings...)
		catalog[i] = entry
	}
	return catalog
}
