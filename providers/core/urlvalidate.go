package core

import (
	"fmt"
	"net/url"
	"path"
	"strings"
)

// ValidateBaseURL reports an error when rawURL is not a valid absolute HTTP(S)
// URL with a host. Providers call it from their constructors so a misconfigured
// base URL fails fast at startup instead of producing malformed upstream
// requests. name is the provider identifier, included in the error for
// diagnostics.
func ValidateBaseURL(name, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%s: invalid base URL %q: must be http or https with a host", name, rawURL)
	}
	return nil
}

// ResolveAPIRoot resolves a configured base URL into the one API root every
// surface of a provider builds its URLs from.
//
// `<PROVIDER>_BASE_URL` is the API ROOT, taken verbatim — the convention every
// official OpenAI-compatible client follows. openai-python and openai-node
// default to "https://api.openai.com/v1" and append only "chat/completions" or
// "embeddings"; LiteLLM delegates to that client and documents that the
// operator supplies the /v1 postfix; Portkey documents the same for a custom
// host. No official client rewrites a base URL its caller gave it.
//
// One exception, and it is narrow: a base with NO path at all adopts the
// version segment defaultRoot ends in. Such a URL is a host, not an API root —
// no versioned HTTP API serves itself at a bare root — so it is unambiguously
// the missing-suffix mistake rather than a deliberate mount point. The segment
// comes from the provider's own default so the fallback cannot disagree with
// it: an OpenAI-wire provider lands on /v1, gemini on /v1beta, and a provider
// whose root carries no version segment gets nothing appended.
//
// Anything carrying a path is the operator's own root and is used unchanged, so
// a proxy mounted at /openai/v1, and one that really does serve the API at
// /openai, are both expressible.
//
// The resolution happens HERE, once, at construction. That is the whole point:
// one stored value, so no request-time code can reinterpret it.
func ResolveAPIRoot(name, configured, defaultRoot string) (string, error) {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return strings.TrimRight(defaultRoot, "/"), nil
	}
	u, err := url.Parse(configured)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("%s: invalid base URL %q: must be http or https with a host", name, configured)
	}
	// A query or fragment cannot survive an operation path being appended to it:
	// "https://h/v1?a=b" + "/chat/completions" is "https://h/v1?a=b/chat/completions",
	// which asks for /v1 with the operation buried in a query value. There is no
	// reading of such a base that produces the request the operator meant, so it
	// is refused here rather than turned into a malformed URL at request time.
	//
	// A bare trailing "#" parses to an empty Fragment, so it is caught by suffix
	// rather than by u.Fragment — the same net as ForceQuery covers a bare "?".
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.HasSuffix(configured, "#") {
		return "", fmt.Errorf("%s: invalid base URL %q: an API root carries no query string or fragment", name, configured)
	}
	if strings.Trim(u.Path, "/") != "" {
		return strings.TrimRight(configured, "/"), nil
	}
	// Userinfo belongs to the host and is preserved: it is how a base URL points
	// at a proxy that authenticates with HTTP Basic, and rebuilding the root
	// without it silently downgrades an authenticated egress path to an
	// anonymous one.
	root := url.URL{Scheme: u.Scheme, User: u.User, Host: u.Host, Path: versionSegment(defaultRoot)}
	return root.String(), nil
}

// versionSegment returns the trailing version segment of an API root — "/v1"
// for "https://api.groq.com/openai/v1", "/v1beta" for gemini — or "" when the
// root does not end in one.
func versionSegment(root string) string {
	u, err := url.Parse(root)
	if err != nil {
		return ""
	}
	last := path.Base(strings.TrimRight(u.Path, "/"))
	if len(last) < 2 || last[0] != 'v' || last[1] < '0' || last[1] > '9' {
		return ""
	}
	return "/" + last
}
