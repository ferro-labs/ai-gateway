package repository

import (
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strings"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/internal/redact"
	"github.com/ferro-labs/ai-gateway/plugin"
)

// isEnvRef reports whether s is a bare environment-variable reference of the
// form "${VAR}" (start "${"  end "}"). These are template placeholders that
// survive to the serialized config intentionally; the value they reference is
// resolved from the process environment at runtime and never stored.
func isEnvRef(s string) bool {
	if len(s) <= 3 || s[0] != '$' || s[1] != '{' || s[len(s)-1] != '}' {
		return false
	}
	name := s[2 : len(s)-1]
	if len(name) == 0 {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_' || ('A' <= r && r <= 'Z') || ('a' <= r && r <= 'z'):
			// valid at any position
		case i > 0 && '0' <= r && r <= '9':
			// digits valid after first char
		default:
			return false
		}
	}
	return true
}

// redactedPlaceholder is substituted for any literal secret value when a Config
// is serialized to an admin API response.
const redactedPlaceholder = "[REDACTED]"

// redactValue withholds one value. Any literal string becomes the placeholder.
//
// An empty string has nothing to withhold, and a ${VAR} reference names a value
// rather than carrying one — the value it names is resolved from the process
// environment when the component is constructed and is never stored. Both are
// returned as they stand.
func redactValue(v string) string {
	if v == "" || isEnvRef(v) {
		return v
	}
	return redactedPlaceholder
}

// shownKeys returns the predicate deciding which keys of one free-form map are
// settings an operator may read back. Everything it declines is withheld.
//
// A free-form map is operator-supplied and, with one exception, is handed
// straight to something outside the gateway: a plugin, an exporter, an MCP
// server, an OTLP collector. The gateway cannot read those keys, so it cannot
// tell a setting from a credential — unless the receiver declares its own
// schema. Only plugins do. Everything else is withheld.
//
// The judgement used to run the other way: a value was withheld when its key
// matched a vocabulary of credential-ish fragments and served otherwise. That
// vocabulary cannot be completed — pat, sid, jwt, hmac, otp, dsn, nonce,
// account_sid, connection_string and every non-English spelling name a
// credential and match no fragment — and it failed in the expensive direction,
// because a key nobody had enumerated was served in plaintext to any caller
// holding a read-only key. Withholding by default fails the cheap way instead:
// the most it costs is the legibility of a setting nobody declared.
//
// owner is a snapshot of the struct holding the field, taken before any of its
// fields were rewritten, because one field's policy can depend on a sibling's
// value. field is the field's Go name.
func shownKeys(owner any, field string) func(string) bool {
	switch o := owner.(type) {
	case config.Config:
		// Aliases is the exception: it is not passed to anyone. The gateway
		// resolves it itself and both halves of an entry are model names, so
		// there is no third party for a credential to be destined for.
		if field == "Aliases" {
			return everyKey
		}
	case config.PluginConfig:
		if field == "Config" {
			return declaredSettings(o.Name)
		}
	}
	return noKey
}

func everyKey(string) bool { return true }

func noKey(string) bool { return false }

// declaredSettings returns the predicate for one plugin's config block: a key
// the plugin declares it reads is a setting, and every other key is withheld.
//
// plugin.Builtins is the declaration, and plugin/catalog_test.go holds each
// entry's Settings equal to the keys that package actually reads out of its
// config map — so this allowlist cannot drift from the code it describes, and a
// plugin that gains a setting gains it here with no edit.
//
// A plugin with no catalog entry — which is every plugin registered outside
// this repository — declares nothing, so nothing under it is shown. The gateway
// does not know that plugin's schema, and saying so is more honest than
// guessing at its key names.
func declaredSettings(name string) func(string) bool {
	for _, entry := range plugin.Builtins() {
		if entry.Name != name {
			continue
		}
		declared := make(map[string]struct{}, len(entry.Settings))
		for _, key := range entry.Settings {
			declared[key] = struct{}{}
		}
		return func(key string) bool {
			_, ok := declared[key]
			return ok
		}
	}
	return noKey
}

// scrubFreeFormMap replaces v with a new map of the same type, so the result
// never aliases the live config.
//
// A key shown declares a setting keeps its value's shape and loses only its
// secret parts, exactly as a named scalar field does — so an operator who
// inlined a real credential under a declared key is still covered by the value
// rules. Every other key is withheld, and that judgement covers the whole value
// beneath it at any depth and in any container type.
//
// A map whose keys are not strings has no name to judge and is withheld whole.
//
// Withholding covers the KEY, not only the value beneath it. Redacting the
// value alone served the key verbatim, and that is a disclosure in its own
// right: an operator inlining a credential into a free-form map can put it in
// either position, and `{"sk-…": 60}` came back intact to any caller holding a
// read_only key while the string beside it was correctly `[REDACTED]` — which
// is exactly what made the response look safe. It also meant
// mcp_servers[].env, .headers and observability.exporters[].config, whose
// contract is to show NOTHING, returned every variable and header NAME.
//
// It also covers a value that is not a string. mapStrings rewrites strings and
// copies everything else through, so a number or a bool under a withheld key
// was served as it stood.
//
// The cost is real and is the reason this is a breaking change: a withheld map
// no longer round-trips through the config editor, because the key an operator
// would edit is gone along with the value. `${VAR}` references under a withheld
// key go with it. A shown key still keeps its shape and its references, which
// is where the editable case now lives.
//
// The substitute key is indexed over the SORTED withheld names so the response
// is stable: Go randomises map iteration, and an endpoint returning a different
// body each call is neither diffable nor testable. The index preserves how many
// settings were withheld without naming any of them.
func scrubFreeFormMap(v reflect.Value, shown func(string) bool) {
	if v.IsNil() {
		return
	}
	named := v.Type().Key().Kind() == reflect.String

	position := make(map[string]int)
	if named {
		withheld := make([]string, 0, v.Len())
		for iter := v.MapRange(); iter.Next(); {
			if !shown(iter.Key().String()) {
				withheld = append(withheld, iter.Key().String())
			}
		}
		sort.Strings(withheld)
		for i, key := range withheld {
			position[key] = i
		}
	}

	out := reflect.MakeMapWithSize(v.Type(), v.Len())
	for iter := v.MapRange(); iter.Next(); {
		switch {
		case named && shown(iter.Key().String()):
			out.SetMapIndex(iter.Key(), mapStrings(iter.Value(), scrubScalar))
		case !named:
			// No name to judge and no string key to substitute; the value is
			// still withheld whole.
			out.SetMapIndex(iter.Key(), mapStrings(iter.Value(), redactValue))
		default:
			key := reflect.New(v.Type().Key()).Elem()
			key.SetString(withheldKeyName(position[iter.Key().String()]))
			out.SetMapIndex(key, withheldValue(iter.Value()))
		}
	}
	v.Set(out)
}

// withheldKeyName is the placeholder standing in for one withheld key. It
// carries redactedMarker, so RedactedSecretField refuses a body that round-trips
// one back rather than letting it be written as a real setting name.
func withheldKeyName(i int) string {
	return fmt.Sprintf("%s_KEY_%d]", strings.TrimSuffix(redactedPlaceholder, "]"), i)
}

// withheldValue withholds the value under a withheld key, at any depth.
//
// It differs from mapStrings(redactValue) in one way that matters: a value that
// is not a string is withheld too. mapStrings rewrites strings and copies
// everything else through, so a number or a bool under a withheld key was
// served as it stood.
//
// A ${VAR} reference survives, which is the same rule redactValue applies and
// is load-bearing beyond tidiness. warnStoredLiterals decides whether an
// operator inlined a credential by scrubbing the config and asking what
// changed; replacing a reference here would make that warning fire on every
// correctly-written config — precisely the ones that took the advice the
// warning gives. An environment variable's NAME is not a credential, and the
// key it sat under is withheld regardless.
func withheldValue(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.String:
		out := reflect.New(v.Type()).Elem()
		out.SetString(redactValue(v.String()))
		return out
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			return v
		}
		switch v.Elem().Kind() {
		case reflect.String, reflect.Map, reflect.Slice, reflect.Interface, reflect.Pointer:
			inner := withheldValue(v.Elem())
			if inner.Type().AssignableTo(v.Type()) {
				return inner
			}
			out := reflect.New(v.Type()).Elem()
			out.Set(inner)
			return out
		default:
			// A number or a bool boxed in an `any`, which is what a free-form
			// map decodes to. Substituting at this level is what lets the marker
			// replace it: the concrete type below cannot hold a string, so
			// recursing would zero the value instead of withholding it.
			return markerAs(v.Type())
		}
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		for iter := v.MapRange(); iter.Next(); {
			out.SetMapIndex(iter.Key(), withheldValue(iter.Value()))
		}
		return out
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := range v.Len() {
			out.Index(i).Set(withheldValue(v.Index(i)))
		}
		return out
	default:
		// A number, a bool: nothing a string can be assigned to, so it is
		// replaced by the placeholder where the type allows and zeroed where it
		// does not. Either way it stops being the operator's value.
		return markerAs(v.Type())
	}
}

// markerAs returns the placeholder typed as t, or t's zero value when a string
// cannot be assigned to it. Either way the operator's value does not survive.
func markerAs(t reflect.Type) reflect.Value {
	marker := reflect.ValueOf(redactedPlaceholder)
	if marker.Type().AssignableTo(t) {
		out := reflect.New(t).Elem()
		out.Set(marker)
		return out
	}
	return reflect.Zero(t)
}

// mapStrings returns a copy of v with scrub applied to every string anywhere
// inside it.
//
// Maps and slices are rebuilt as new containers of the same type whatever their
// element types are, so nothing aliases the live config and no concrete type
// has to be enumerated. Enumeration is what let the old rule apply to
// map[string]any and walk straight past the map[string]string beside it.
//
// Anything that is neither a string nor a container is copied as it stands: a
// free-form map is decoded from JSON or YAML, so it holds only those kinds.
func mapStrings(v reflect.Value, scrub func(string) string) reflect.Value {
	switch v.Kind() {
	case reflect.String:
		out := reflect.New(v.Type()).Elem()
		out.SetString(scrub(v.String()))
		return out
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			return v
		}
		return mapStrings(v.Elem(), scrub)
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		for iter := v.MapRange(); iter.Next(); {
			out.SetMapIndex(iter.Key(), mapStrings(iter.Value(), scrub))
		}
		return out
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := range v.Len() {
			out.Index(i).Set(mapStrings(v.Index(i), scrub))
		}
		return out
	default:
		return v
	}
}

// scrubArgs returns a new slice with the value positions of a stdio command
// line redacted. The original slice is not modified.
//
// A command line carries credentials as routinely as a header does — a token
// sits either in the element after its flag ("--api-key", "sk-…") or joined to
// it ("--api-key=sk-…") — so every element that can hold a value is treated as
// one. An element beginning with "-" is a flag name and is kept, up to and
// including the "=" that separates it from an inline value; everything else is
// a value position and is redacted whole. Position is all there is to go on
// here: unlike a map entry, an argument carries no key whose name could say
// whether it holds a credential.
//
// The split is what keeps the result legible. Redacting whole elements would
// leave an operator unable to tell one server's command line from another's,
// and a flag name is not a secret. Keeping every element that is not obviously
// a value would leak the separate form, which is where a token most often sits.
func scrubArgs(args []string) []string {
	if args == nil {
		return nil
	}
	out := make([]string, len(args))
	for i, arg := range args {
		name, value, inline := strings.Cut(arg, "=")
		switch {
		case !strings.HasPrefix(arg, "-"):
			out[i] = redactValue(arg)
		case inline:
			out[i] = name + "=" + redactValue(value)
		default:
			out[i] = arg
		}
	}
	return out
}

// containsRedacted reports whether a redaction token appears in a string value
// anywhere inside v, walking maps and slices of any element type. It mirrors the
// coverage of scrubAnyValue rather than enumerating the same concrete cases,
// because anything the scrubber can write out is something an operator can send
// straight back.
//
// Containment, not equality: a map value whose key is innocuous keeps its shape
// and loses only its secret parts, exactly as a named scalar field does, so it
// comes back as "Bearer [REDACTED_BEARER_TOKEN]" or a URL with one query value
// replaced rather than as the bare placeholder. Testing for equality would let
// precisely those through to overwrite the live credential.
// keys decides whether map KEYS count. They do for the write guard, which must
// refuse a body round-tripping a placeholder back as a real setting name. They
// do not for warnStoredLiterals, which asks whether scrubbing changed a VALUE —
// whether the operator inlined a literal — and every free-form key is withheld
// by default, so counting keys would fire that warning on every config that
// took its own advice and used ${VAR}.
func containsRedactedScoped(v any, keys bool) bool {
	if v == nil {
		return false
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.String:
		return strings.Contains(rv.String(), redactedMarker)
	case reflect.Map:
		iter := rv.MapRange()
		for iter.Next() {
			// The KEY is tested as well as the value. A withheld free-form entry
			// now comes back with its key replaced too, so a caller round-tripping
			// GET into PUT without fixing it up would otherwise write the marker
			// itself as a real setting name.
			if keys && iter.Key().Kind() == reflect.String && strings.Contains(iter.Key().String(), redactedMarker) {
				return true
			}
			if containsRedactedScoped(iter.Value().Interface(), keys) {
				return true
			}
		}
	case reflect.Slice, reflect.Array:
		for i := range rv.Len() {
			if containsRedactedScoped(rv.Index(i).Interface(), keys) {
				return true
			}
		}
	case reflect.Interface, reflect.Pointer:
		if !rv.IsNil() {
			return containsRedactedScoped(rv.Elem().Interface(), keys)
		}
	}
	return false
}

// redactedMarker is the prefix every redaction token starts with — the config
// placeholder "[REDACTED]" and every internal/redact token ("[REDACTED_JWT]",
// "[REDACTED:OPENAI_API_KEY]", …). Scalar fields are tested by containment
// because a scrubbed value keeps its surrounding text, e.g. a URL retains its
// host and only the query value is replaced.
const redactedMarker = "[REDACTED"

// RedactedSecretField returns the config path of the first field holding a
// redaction token, or "" when none does.
//
// GET /admin/config replaces literal secrets with a placeholder. An operator who
// edits the response and sends it back — which the dashboard's config editor
// makes a single click — would otherwise overwrite live credentials with the
// placeholder text, breaking the integration with no error and no way to
// recover the original value. It walks the same struct ScrubConfigSecrets
// walks, so whatever is scrubbed on the way out is refused on the way in and
// the two cannot drift.
//
// Map KEYS count as well as values, because a withheld entry now comes back
// with its key replaced too.
func RedactedSecretField(cfg config.Config) string {
	return redactedPath(reflect.ValueOf(cfg), "", true)
}

// RedactedLiteralField is RedactedSecretField restricted to VALUES. It answers
// the question warnStoredLiterals asks — did scrubbing withhold something the
// operator wrote as a literal — where a withheld KEY is not evidence, because
// every undeclared free-form key is withheld by default.
func RedactedLiteralField(cfg config.Config) string {
	return redactedPath(reflect.ValueOf(cfg), "", false)
}

// redactedPath returns the JSON path of the first redaction token under v.
//
// A token inside a map, or inside a slice of strings, reports the path of the
// field that holds the container rather than the individual key or index: the
// operator's unit of repair is the whole `plugins[0].config` block, not one key
// inside it, and that also keeps the reported path stable as map iteration
// order varies.
func redactedPath(v reflect.Value, path string, keys bool) string {
	switch v.Kind() {
	case reflect.String:
		if strings.Contains(v.String(), redactedMarker) {
			return path
		}
	case reflect.Struct:
		for i := range v.NumField() {
			f := v.Type().Field(i)
			name, ok := jsonFieldName(f)
			if !ok {
				continue
			}
			if p := redactedPath(v.Field(i), joinPath(path, name), keys); p != "" {
				return p
			}
		}
	case reflect.Slice, reflect.Array:
		elemPath := func(i int) string { return fmt.Sprintf("%s[%d]", path, i) }
		if v.Type().Elem().Kind() == reflect.String {
			elemPath = func(int) string { return path }
		}
		for i := range v.Len() {
			if p := redactedPath(v.Index(i), elemPath(i), keys); p != "" {
				return p
			}
		}
	case reflect.Map:
		if !v.IsNil() && containsRedactedScoped(v.Interface(), keys) {
			return path
		}
	case reflect.Interface, reflect.Pointer:
		if !v.IsNil() {
			return redactedPath(v.Elem(), path, keys)
		}
	}
	return ""
}

// jsonFieldName returns the serialized name of an exported struct field, and
// false for a field that is unexported or explicitly excluded with `json:"-"`.
func jsonFieldName(f reflect.StructField) (string, bool) {
	if !f.IsExported() {
		return "", false
	}
	tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	switch tag {
	case "-":
		return "", false
	case "":
		return f.Name, true
	}
	return tag, true
}

func joinPath(base, name string) string {
	if base == "" {
		return name
	}
	return base + "." + name
}

// ScrubConfigSecrets returns a copy of cfg with secret material replaced by
// "[REDACTED]". The live in-memory Config is never mutated.
//
// It walks the Config struct by reflection rather than naming fields, so a
// field added to the schema later is covered without an edit here. That is the
// whole point: mcp_servers[].url was leaking an access token in its query
// string precisely because it was absent from a hand-maintained list, sitting
// beside a properly redacted header.
//
// What is replaced is decided by what holds a secret, never by a list of field
// names kept here:
//
//   - A scalar string field keeps its shape and loses only its secret parts:
//     URL userinfo and query values are replaced, and the text is then run
//     through internal/redact, which removes every credential the gateway holds
//     plus the recognised credential shapes. So "virtual_key: openai" and
//     "mode: fallback" survive intact while an access token in a URL does not.
//   - A free-form map — the operator-supplied surface of headers, subprocess
//     env, plugin and exporter config — is withheld key by key unless the
//     receiver declares that key a setting. See shownKeys. A shown key's value
//     still goes through the scalar rules above.
//
// Args is the one field consulted by name, because a stdio command line is
// positional and its value positions carry no key to judge: see scrubArgs.
// "${ENV_VAR}" references are preserved everywhere — they contain no secret
// material, and the value is resolved from the process environment at
// construction time.
func ScrubConfigSecrets(cfg config.Config) config.Config {
	scrubValue(reflect.ValueOf(&cfg).Elem(), "")
	return cfg
}

// scrubValue rewrites v in place. v must be addressable; field is the Go name
// of the struct field that produced v, used only for the Args special case.
//
// Maps and slices are replaced with fresh containers rather than written
// through, so the returned Config never aliases the live one.
func scrubValue(v reflect.Value, field string) {
	switch v.Kind() {
	case reflect.String:
		v.SetString(scrubScalar(v.String()))
	case reflect.Struct:
		scrubStruct(v)
	case reflect.Slice:
		if v.IsNil() {
			return
		}
		if field == "Args" {
			if args, ok := v.Interface().([]string); ok {
				v.Set(reflect.ValueOf(scrubArgs(args)))
				return
			}
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		reflect.Copy(out, v)
		for i := range out.Len() {
			scrubValue(out.Index(i), field)
		}
		v.Set(out)
	case reflect.Map:
		// A map the walk reaches other than as a struct field has no owner to
		// declare a schema for it, so it is withheld whole. Unrecognised means
		// unknown, and unknown is not served.
		scrubFreeFormMap(v, noKey)
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			scrubValue(v.Elem(), field)
		}
	}
}

// scrubStruct walks a struct's exported fields, handing each free-form map the
// policy its owner declares for that field.
//
// The owner is snapshotted before the loop rewrites anything, because a field's
// policy can depend on a sibling's value: a plugin's config block is judged
// against the settings its Name declares, and Name is rewritten first.
func scrubStruct(v reflect.Value) {
	owner := v.Interface()
	for i := range v.NumField() {
		f := v.Type().Field(i)
		if !f.IsExported() {
			continue
		}
		if field := v.Field(i); field.Kind() == reflect.Map {
			scrubFreeFormMap(field, shownKeys(owner, f.Name))
		} else {
			scrubValue(field, f.Name)
		}
	}
}

// scrubScalar redacts the secret-bearing parts of a named string field while
// leaving the rest legible.
func scrubScalar(s string) string {
	if s == "" || isEnvRef(s) {
		return s
	}
	return redact.String(scrubURLSecrets(s))
}

// scrubURLSecrets replaces the credential-bearing parts of a URL — the password
// half of userinfo and every query-parameter value — leaving scheme, host and
// path readable.
//
// A URL is the one scalar shape that routinely carries a credential inline
// while still needing to be recognisable: an operator has to be able to tell
// which MCP server or which collector is configured. Blanking the whole field
// would answer the leak by making the served config useless.
func scrubURLSecrets(s string) string {
	if !strings.Contains(s, "://") {
		return s
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" {
		return s
	}

	changed := false
	if u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword {
			u.User = url.UserPassword(u.User.Username(), redactedPlaceholder)
			changed = true
		}
	}
	if u.RawQuery != "" {
		if values, qerr := url.ParseQuery(u.RawQuery); qerr == nil {
			for key, vals := range values {
				for i, val := range vals {
					if val == "" || isEnvRef(val) {
						continue
					}
					values[key][i] = redactedPlaceholder
					changed = true
				}
			}
			u.RawQuery = values.Encode()
		}
	}
	if !changed {
		return s
	}
	// url.String percent-encodes the placeholder's brackets in both the
	// userinfo and query positions; restore the literal so the marker stays
	// recognisable to RedactedSecretField and to a reader.
	return strings.ReplaceAll(u.String(), url.QueryEscape(redactedPlaceholder), redactedPlaceholder)
}
