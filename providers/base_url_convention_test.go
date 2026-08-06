package providers_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// providerFunc is one function declared in a provider subpackage, handed to a
// scan callback along with the package directory it came from.
type providerFunc struct {
	pkg  string // provider subpackage directory, e.g. "groq"
	fn   *ast.FuncDecl
	fset *token.FileSet
}

// scanProviderFuncs parses every non-test source file under providers/<id>/ and
// calls visit for each function that is NOT a constructor.
//
// A constructor is exactly where base-URL resolution belongs, so both guards
// below exempt it: New (and the named resolvers a constructor delegates to) run
// once, at build time, and produce the single stored root.
func scanProviderFuncs(t *testing.T, visit func(providerFunc)) {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read providers dir: %v", err)
	}

	scanned := 0
	fset := token.NewFileSet()
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		files, err := filepath.Glob(filepath.Join(entry.Name(), "*.go"))
		if err != nil {
			t.Fatalf("glob %s: %v", entry.Name(), err)
		}
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			src, err := os.ReadFile(path) //nolint:gosec // test-time read of repo sources
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			file, err := parser.ParseFile(fset, path, src, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			scanned++

			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				if fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "New") {
					continue
				}
				// Named resolvers a constructor delegates to are the constructor
				// for this purpose.
				if fn.Name.Name == "normalizeBaseURL" {
					continue
				}
				visit(providerFunc{pkg: entry.Name(), fn: fn, fset: fset})
			}
		}
	}

	// A guard that silently scanned nothing is worse than no guard: it reports
	// green for a tree it never read.
	if scanned < 20 {
		t.Fatalf("scanned only %d provider source files; the scan is not reaching providers/<id>/", scanned)
	}
}

// TestBaseURLIsResolvedOnlyAtConstruction is the drift guard a NEW provider
// inherits without doing anything.
//
// The defect it exists to prevent: one configured base URL interpreted two ways
// inside a single provider. The openai adapter reaches its upstream two ways —
// chat by hand-built HTTP, embeddings and images through the vendor SDK — and
// the hand-built path used to append /v1 when the base lacked it while the SDK
// treated the base as the API root. So OPENAI_BASE_URL=http://host:9901 sent
// chat to /v1/chat/completions and embeddings to /embeddings: one worked, one
// 404'd, and the failure surfaced as an opaque upstream error.
//
// A provider resolves its base URL once, in its constructor, and every surface
// then appends only its operation path. Request-time code that re-inspects the
// base's shape is the second interpretation, and it is what this test bans.
func TestBaseURLIsResolvedOnlyAtConstruction(t *testing.T) {
	// What is banned is BRANCHING on the base URL's shape — asking whether it
	// already carries a segment and building a different URL depending on the
	// answer. That is what turns one configured value into two conventions.
	//
	// Unconditional derivation (strings.TrimSuffix, strings.TrimRight) is not
	// banned: it maps every input the same way, so it cannot produce two roots
	// from one base. hugging_face's routerRoot derives a sibling root that way,
	// deliberately and deterministically.
	const banned = "strings.HasSuffix or strings.HasPrefix applied to a base URL"

	scanProviderFuncs(t, func(pf providerFunc) {
		ast.Inspect(pf.fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "strings" {
				return true
			}
			switch sel.Sel.Name {
			case "HasSuffix", "HasPrefix":
			default:
				return true
			}
			if !mentionsBaseURL(call.Args[0]) {
				return true
			}
			t.Errorf("%s: %s.%s in %s inspects the base URL at request time; %s must not appear outside the constructor. "+
				"Resolve the base once in New and store the canonical root, so every surface appends only its operation path.",
				pf.fset.Position(call.Pos()), pkg.Name, sel.Sel.Name, pf.fn.Name.Name, banned)
			return true
		})
	})
}

// hostRootProviders are the providers whose configured base is deliberately NOT
// an API root, mapped to the reason. Each one appends a version segment at
// request time, which is exactly what the guard below bans, so each needs an
// entry here and each entry has to earn it.
//
// The bar: the configured value is genuinely not an API root — either because
// the vendor publishes no single root for every surface to hang off, or because
// the knob names a resource host from which the vendor's surface path is fixed
// and not the operator's to choose.
func hostRootProviders() map[string]string {
	return map[string]string{
		"cohere": "The two surfaces the gateway uses sit on different versions of Cohere's API: chat on " +
			"/v2/chat and embeddings on /v1/embed. There is therefore no one root to store, even though " +
			"Cohere does serve /v2/embed — moving embeddings onto it is a response-schema migration, not a " +
			"URL edit, so until that is done COHERE_BASE_URL stays the host.",
		"ollama": "OLLAMA_HOST is Ollama's own variable for the server root, not an API root, and one " +
			"Ollama server mounts two of them: the OpenAI-compatible surface at /v1 and the native API at /api.",
		"azure_foundry": "AZURE_FOUNDRY_ENDPOINT is the Azure resource host, the value the portal shows, and " +
			"the surface path under it is Azure's own fixed shape rather than a mount point the operator picks. " +
			"The GA OpenAI-compatible route is /openai/v1, so the version segment sits mid-path and travels with " +
			"the vendor prefix that is not the operator's to write.",
	}
}

// TestBaseURLGainsNoVersionSegmentAtRequestTime enforces the contract itself,
// which the guard above does not: `<PROVIDER>_BASE_URL` is the API root, so a
// surface appends only its operation path to it.
//
// The guard above bans *branching* on the base's shape. Appending "/v1/..."
// unconditionally does not branch — every surface of the provider agrees, so
// that guard passes — and yet it makes the one variable mean something
// different than it does on the provider next door: the operator has to know,
// per provider, whether to write the /v1 or leave it off, and writing it on the
// wrong one silently produces /v1/v1/chat/completions.
//
// So: a version segment belongs to the root, which is resolved once in New
// (core.ResolveAPIRoot), never re-appended per request.
func TestBaseURLGainsNoVersionSegmentAtRequestTime(t *testing.T) {
	allowed := hostRootProviders()
	seen := make(map[string]bool, len(allowed))

	report := func(pf providerFunc, pos token.Pos, path string) {
		if _, ok := allowed[pf.pkg]; ok {
			seen[pf.pkg] = true
			return
		}
		t.Errorf("%s: %s appends %q to the base URL at request time. "+
			"The base URL is the API ROOT: fold the version segment into the provider's default base and into "+
			"core.ResolveAPIRoot's result in New, then append only the operation path here. "+
			"A provider whose vendor publishes no single root belongs in hostRootProviders() with the reason.",
			pf.fset.Position(pos), pf.fn.Name.Name, path)
	}

	scanProviderFuncs(t, func(pf providerFunc) {
		ast.Inspect(pf.fn.Body, func(n ast.Node) bool {
			switch node := n.(type) {
			// p.baseURL + "/v1/chat/completions"
			case *ast.BinaryExpr:
				if node.Op != token.ADD || !mentionsBaseURL(node.X) {
					return true
				}
				if lit := stringLit(node.Y); carriesVersionSegment(lit) != "" {
					report(pf, node.OpPos, lit)
				}
			// fmt.Sprintf("%s/v1beta/models/%s:generateContent", p.baseURL, model)
			case *ast.CallExpr:
				if len(node.Args) < 2 || !isSprintf(node.Fun) {
					return true
				}
				format := stringLit(node.Args[0])
				verb := formatVersionSegment(format)
				if verb == "" {
					return true
				}
				for _, arg := range node.Args[1:] {
					if mentionsBaseURL(arg) {
						report(pf, node.Lparen, verb)
						break
					}
				}
			}
			return true
		})
	})

	// An allowlist nobody prunes is how the exception outlives its reason.
	for pkg, reason := range allowed {
		if !seen[pkg] {
			t.Errorf("provider %q is listed in hostRootProviders() (%s) but no longer appends a version segment "+
				"to its base at request time; drop the entry so the exception does not outlive its reason.", pkg, reason)
		}
	}
}

// TestCarriesVersionSegment pins what the two guards above count as a version
// segment, because both of them act on its answer and neither can see a wrong
// one: a miss lets a provider re-supply a version the root already owns, and a
// false positive bans an operation path that was always legitimate.
func TestCarriesVersionSegment(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		// The head segment, which is what the guards used to read.
		{"/v1/chat/completions", "v1"},
		{"/v2/chat", "v2"},
		{"/v1beta/models", "v1beta"},

		// Anywhere else, which is what they missed. azure-foundry's GA route is
		// the in-tree example, and it passed both guards green.
		{"/openai/v1/chat/completions", "v1"},
		{"/openai/deployments/d/v1/chat/completions", "v1"},

		// A model id or resource name that merely opens like a version is not
		// one. These travel in operation paths and must not be banned.
		{"/models/stable-diffusion-v1-5", ""},
		{"/models/v1_5/predict", ""},
		{"/versions/latest", ""},
		{"/chat/completions", ""},
		{"/embeddings", ""},
	}

	for _, tc := range cases {
		if got := carriesVersionSegment(tc.path); got != tc.want {
			t.Errorf("carriesVersionSegment(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// stringLit returns the value of an untyped string literal, or "" for anything
// else.
func stringLit(expr ast.Expr) string {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	unquoted, err := strconv.Unquote(lit.Value)
	if err != nil {
		return ""
	}
	return unquoted
}

// isSprintf reports whether expr names fmt.Sprintf.
func isSprintf(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Sprintf" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "fmt"
}

// carriesVersionSegment returns the version segment an operation path contains,
// or "" for a path that contains none.
//
// Every segment counts, not only the first: "/v1/chat/completions" and
// "/openai/v1/chat/completions" both hand the root a version the root already
// owns, and both double it for an operator who wrote the /v1 the vendor
// documents. Reading only the leading segment declares the second one clean.
func carriesVersionSegment(path string) string {
	for _, segment := range strings.Split(path, "/") {
		if isVersionSegment(segment) {
			return segment
		}
	}
	return ""
}

// formatVersionSegment returns the version-carrying path a Sprintf format
// appends to a "%s" placeholder, or "" when it appends none. It is the Sprintf
// spelling of the concatenation above — "%s/v1beta/models/%s:predict" builds
// the same URL as p.baseURL + "/v1beta/models/...".
func formatVersionSegment(format string) string {
	for i := 0; i+2 < len(format); i++ {
		if format[i:i+2] != "%s" {
			continue
		}
		// Stop at the next placeholder: the segments after it are built from a
		// runtime value, so a "v1"-shaped literal beyond it is not something the
		// provider is appending to the root.
		rest := format[i+2:]
		if next := strings.Index(rest, "%"); next >= 0 {
			rest = rest[:next]
		}
		if carriesVersionSegment(rest) != "" {
			return rest
		}
	}
	return ""
}

// mentionsBaseURL reports whether an expression names a base-URL value, which is
// how these guards tell `strings.HasSuffix(p.baseURL, "/v1")` from the many
// legitimate string checks a provider makes on models, roles and finish reasons.
func mentionsBaseURL(expr ast.Expr) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		var name string
		switch v := n.(type) {
		case *ast.Ident:
			name = v.Name
		case *ast.SelectorExpr:
			name = v.Sel.Name
		default:
			return true
		}
		if strings.Contains(strings.ToLower(name), "baseurl") {
			found = true
			return false
		}
		return true
	})
	return found
}
