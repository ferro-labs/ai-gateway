// Package envref substitutes ${VAR} environment references in configuration
// values.
//
// Resolution happens at the point a value is USED — when a plugin, exporter, or
// MCP client is constructed — never when the Config is loaded. That distinction is
// the whole point of this package: a Config that carried materialised secrets would
// leak them everywhere it travels, and it travels a long way. It is persisted to the
// config-history store, returned by GET /admin/config, and restored on rollback.
// Keeping ${VAR} in the Config and resolving only into the constructed component
// means a secret is never written to a database or served over the admin API, while
// the component still receives the real value.
//
// Resolving at use also means a ${VAR} pushed through the admin/GitOps config API —
// which never passes through LoadConfig — is substituted just the same.
package envref

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// refPattern matches an explicit ${VAR} reference.
//
// ONLY the braced form is a reference. A bare "$" is data, not a template: a price
// ("costs $5"), a generated password ("pa$$w0rd"), and a guardrail's blocked word
// ("$100") must survive byte-for-byte. os.Expand cannot do this — it reads $1, $$
// and $w0rd as shell variables and silently eats them, which corrupts word lists and
// mangles secrets.
var refPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Expand substitutes every ${VAR} in s.
//
// An undefined variable is an operator error, not a default: Expand returns an error
// naming it rather than substituting "". Silently blanking a value turns a secret into
// a baffling upstream auth failure and a guardrail's blocked word into a rule that
// matches nothing — failures that surface far from their cause.
func Expand(s string) (string, error) {
	var missing []string
	out := refPattern.ReplaceAllStringFunc(s, func(ref string) string {
		name := refPattern.FindStringSubmatch(ref)[1]
		val, ok := os.LookupEnv(name)
		if !ok {
			missing = append(missing, name)
			return ref
		}
		return val
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("undefined environment variable(s): %s", strings.Join(missing, ", "))
	}
	return out, nil
}

// StringMap returns a copy of m with every value expanded. The input is never
// mutated: the caller's Config must keep its ${VAR} references.
func StringMap(m map[string]string) (map[string]string, error) {
	if len(m) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		resolved, err := Expand(v)
		if err != nil {
			return nil, fmt.Errorf("key %q: %w", k, err)
		}
		out[k] = resolved
	}
	return out, nil
}

// AnyMap returns a deep copy of m with every string value expanded, recursing into
// nested maps and slices. The input is never mutated.
func AnyMap(m map[string]any) (map[string]any, error) {
	if m == nil {
		return nil, nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		resolved, err := anyValue(v)
		if err != nil {
			return nil, fmt.Errorf("key %q: %w", k, err)
		}
		out[k] = resolved
	}
	return out, nil
}

func anyValue(v any) (any, error) {
	switch val := v.(type) {
	case string:
		return Expand(val)
	case map[string]any:
		return AnyMap(val)
	case map[string]string:
		return StringMap(val)
	case []any:
		out := make([]any, len(val))
		for i, elem := range val {
			resolved, err := anyValue(elem)
			if err != nil {
				return nil, fmt.Errorf("index %d: %w", i, err)
			}
			out[i] = resolved
		}
		return out, nil
	case []string:
		out := make([]string, len(val))
		for i, elem := range val {
			resolved, err := Expand(elem)
			if err != nil {
				return nil, fmt.Errorf("index %d: %w", i, err)
			}
			out[i] = resolved
		}
		return out, nil
	default:
		return v, nil
	}
}
