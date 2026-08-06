package redact

import (
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
)

// MinSecretLength is the shortest configured value that is redacted by value.
//
// A value-based redactor replaces literal text wherever it appears, so a short
// or placeholder credential would delete ordinary words from every log line the
// gateway writes: with OPENAI_API_KEY=test, "test" vanishes from "test request
// failed". CI runners hit this first and all landed on a length floor —
// Buildkite refuses values under 6 bytes for exactly this reason, GitLab under
// 8, CircleCI under 4 (and additionally excludes the literals true/false).
//
// 12 is deliberately stricter than any of those. Their output is build logs;
// ours is application logs, where ordinary English prose is far likelier to
// collide. 12 sits above every plausible placeholder ("changeme", "secret",
// "password", "localhost") and well below the shortest real provider
// credential — an AWS access key ID is 20 characters, and every provider API
// key in the matrix is 32 or more.
const MinSecretLength = 12

// genericSecretToken replaces a secret registered without a source name.
const genericSecretToken = "[REDACTED_SECRET]"

// credentialNameParts are the name components that mark a value as a
// credential. The test in this package walks providers.AllProviders() and fails
// if any provider's credential variable is named outside this set, so a new
// provider is enrolled by construction rather than by remembering to edit a list
// here.
//
// AUTHORIZATION is the one entry that names no key, token or secret: it is
// HTTP's own credential header, so it is what an operator writes above a bearer
// token in an MCP server's or an OTLP exporter's headers.
var credentialNameParts = map[string]bool{
	"KEY":           true,
	"KEYS":          true,
	"TOKEN":         true,
	"SECRET":        true,
	"PASSWORD":      true,
	"PASSWD":        true,
	"CREDENTIAL":    true,
	"CREDENTIALS":   true,
	"DSN":           true,
	"AUTHORIZATION": true,
}

// IsCredentialName reports whether a name marks its value as a credential.
//
// The match is on whole components, not substrings, so AWS_SECRET_ACCESS_KEY,
// api_key, x-api-key and Proxy-Authorization all qualify while XAUTHORITY,
// SSH_AUTH_SOCK and MONKEYPATCH do not. Component matching is what keeps an
// unrelated name from enrolling its value and blanking that text out of every
// log line.
//
// Components are split on any non-alphanumeric character and compared
// case-insensitively, so the same judgement serves a SCREAMING_SNAKE
// environment variable, a lower_snake plugin-config key and a Hyphen-Case HTTP
// header. A name that runs its words together — apiKey, accesstoken — has no
// component boundary to split on and is not recognised.
func IsCredentialName(name string) bool {
	for _, part := range strings.FieldsFunc(strings.ToUpper(name), isNameSeparator) {
		if credentialNameParts[part] {
			return true
		}
	}
	return false
}

func isNameSeparator(r rune) bool {
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
}

// isRedactableValue reports whether v is worth matching literally.
//
// Rejected: anything shorter than MinSecretLength; anything containing
// whitespace (a credential does not, and a sentence would blank out prose);
// an unresolved "${VAR}" reference, which is a placeholder rather than a
// secret; and the boolean literals, following CircleCI's exclusion list.
func isRedactableValue(v string) bool {
	if len(v) < MinSecretLength {
		return false
	}
	if strings.ContainsAny(v, " \t\r\n") {
		return false
	}
	if strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}") {
		return false
	}
	switch v {
	case "true", "True", "TRUE", "false", "False", "FALSE":
		return false
	}
	return true
}

// EnvValueIsCredential reports whether an environment variable would be
// enrolled for value redaction — its name marks it as a credential and its
// value clears the floor. It is the predicate the environment scan applies,
// exported so the provider-coverage test can assert that every provider's
// credential variable is covered without that test having to restate the rule.
func EnvValueIsCredential(name, value string) bool {
	return credentialIn(name, value) != ""
}

// credentialIn returns the part of an environment value that is itself a
// secret, or "" when the value holds none.
//
// For almost every credential variable that is the whole value. A DSN is the
// exception, because it names a *location*: API_KEY_STORE_DSN=/data/keys.db is
// a filesystem path, and enrolling it deleted the path from the startup line
// whose entire purpose is to tell the operator which files to back up. What a
// DSN can carry is a password in its userinfo — postgres://user:pw@host — and
// that, not the string around it, is the secret. Extracting it keeps the host
// and database readable in a connection error while the password still cannot
// be printed anywhere. A credential written into the query string instead
// (?access_token=…) is covered by URLCredentials on the paths that see URLs.
func credentialIn(name, value string) string {
	if !IsCredentialName(name) {
		return ""
	}
	if isDSNName(name) {
		value = urlPassword(value)
	}
	if !isRedactableValue(value) {
		return ""
	}
	return value
}

// isDSNName reports whether the variable holds a connection string rather than
// a bare credential. Whole-component match, like IsCredentialName.
func isDSNName(name string) bool {
	for _, part := range strings.Split(strings.ToUpper(name), "_") {
		if part == "DSN" {
			return true
		}
	}
	return false
}

// urlPassword returns the userinfo password of a URL-shaped DSN, or "" for a
// value that carries none — a bare filesystem path, a host:port, or a URL with
// no credentials in it.
func urlPassword(dsn string) string {
	if !strings.Contains(dsn, "://") {
		return ""
	}
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil {
		return ""
	}
	password, _ := u.User.Password()
	return password
}

// valueMatcher replaces a fixed set of literal secrets.
//
// Cost note. replacer is built once; scanning is guarded by strings.Contains
// per secret, which is SIMD-accelerated and allocates nothing, because
// strings.Replacer's multi-pattern implementation allocates an output buffer
// unconditionally — even when nothing matches. The guard keeps the common case
// (a line holding no secret) allocation-free. minLen skips the guard entirely
// for any string shorter than the shortest registered secret, which is most log
// attribute values.
type valueMatcher struct {
	pairs    []string // old1, new1, old2, new2, …
	minLen   int
	replacer *strings.Replacer
}

func newValueMatcher(pairs []string) *valueMatcher {
	if len(pairs) == 0 {
		return &valueMatcher{}
	}
	minLen := len(pairs[0])
	for i := 0; i < len(pairs); i += 2 {
		if l := len(pairs[i]); l < minLen {
			minLen = l
		}
	}
	return &valueMatcher{pairs: pairs, minLen: minLen, replacer: strings.NewReplacer(pairs...)}
}

func (m *valueMatcher) redact(s string) string {
	if m == nil || len(m.pairs) == 0 || len(s) < m.minLen {
		return s
	}
	for i := 0; i < len(m.pairs); i += 2 {
		if strings.Contains(s, m.pairs[i]) {
			return m.replacer.Replace(s)
		}
	}
	return s
}

func (m *valueMatcher) has(v string) bool {
	for i := 0; i < len(m.pairs); i += 2 {
		if m.pairs[i] == v {
			return true
		}
	}
	return false
}

var (
	secretsMu sync.Mutex
	secrets   atomic.Pointer[valueMatcher]
	seedOnce  sync.Once
)

// envSecretPairs builds the replacement table from the process environment.
//
// The environment is the only place a self-hosted gateway's credentials live —
// ProviderConfigFromEnv reads them from there, and internal/envref resolves
// ${VAR} from there — so indexing it creates no second store of secret
// material. It is a derived view of memory the process already holds, built
// lazily on the first redaction and never written anywhere.
//
// Each value maps to a token naming its source variable, so an operator reading
// "invalid api_key: [REDACTED:MISTRAL_API_KEY]" learns which credential the
// upstream rejected without the credential itself being disclosed. A variable
// name is not secret; it is documented.
func envSecretPairs() []string {
	var pairs []string
	seen := make(map[string]bool)
	for _, kv := range os.Environ() {
		name, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		secret := credentialIn(name, value)
		if secret == "" || seen[secret] {
			continue
		}
		seen[secret] = true
		pairs = append(pairs, secret, "[REDACTED:"+name+"]")
	}
	return pairs
}

func seed() {
	seedOnce.Do(func() {
		secretsMu.Lock()
		defer secretsMu.Unlock()
		if secrets.Load() == nil {
			secrets.Store(newValueMatcher(envSecretPairs()))
		}
	})
}

// RegisterSecretValues enrols literal credential values for redaction from
// every string this package processes.
//
// The environment is indexed automatically, so a self-hosted gateway needs no
// call here. It exists for the other supported credential mode: an embedder
// that builds providers.ProviderConfig from a secret manager or a per-tenant
// store, where the credential never passes through an environment variable.
//
// Values failing the MinSecretLength / shape checks are ignored — see
// isRedactableValue. Registration is additive and safe for concurrent use.
func RegisterSecretValues(values ...string) {
	seed()
	secretsMu.Lock()
	defer secretsMu.Unlock()
	cur := secrets.Load()
	pairs := append([]string(nil), cur.pairs...)
	added := false
	for _, v := range values {
		if !isRedactableValue(v) || cur.has(v) {
			continue
		}
		pairs = append(pairs, v, genericSecretToken)
		added = true
	}
	if added {
		secrets.Store(newValueMatcher(pairs))
	}
}

// Values returns s with every registered credential value replaced by a token
// naming its source. It is the cheap half of the egress seam: exact, ordered
// before any pattern rule, and allocation-free when s holds no secret.
//
// Use it — rather than String — on paths that see every line rather than only
// error text; the pattern policies are regex-based and far too costly per line.
func Values(s string) string {
	return active().redact(s)
}

func active() *valueMatcher {
	seed()
	return secrets.Load()
}
