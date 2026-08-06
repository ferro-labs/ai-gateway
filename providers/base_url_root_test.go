package providers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// rootProbe is the mount point every provider in this test is pointed at. It
// carries a path, so ResolveAPIRoot must hand it back verbatim — which makes any
// extra segment in a captured path the provider's own doing and nothing else's.
const (
	rootProbe = "/probe"
)

// probeModels are the two model-id shapes a provider may insist on before it
// will send anything: a plain id, and the owner/name path replicate and
// hugging_face address a model by. Every surface is driven with both, and the
// assertion is on the union, so no provider is skipped for refusing one shape.
var probeModels = []string{"probe-model", "probe/model"}

// pathRecorder answers every request with a 404 and records the path it was
// asked for. The status is deliberate: this test asserts the URL a surface
// builds, not what it does with the answer, and a stub that satisfied five
// different vendors' success payloads would be a second implementation of the
// conformance suite.
type pathRecorder struct {
	*httptest.Server
	mu    sync.Mutex
	paths []string
}

func newPathRecorder(t *testing.T) *pathRecorder {
	t.Helper()
	rec := &pathRecorder{}
	rec.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.paths = append(rec.paths, r.URL.Path)
		rec.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"path recorder","type":"invalid_request_error"}}`))
	}))
	t.Cleanup(rec.Close)
	return rec
}

func (rec *pathRecorder) drain() []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	out := append([]string(nil), rec.paths...)
	rec.paths = nil
	sort.Strings(out)
	return out
}

// exerciseSurfaces drives every surface the provider implements. Errors are
// ignored on purpose: the assertion is about the URL that was requested, and a
// surface that fails still reveals its path.
func exerciseSurfaces(ctx context.Context, p providers.Provider) {
	for _, model := range probeModels {
		_, _ = p.Complete(ctx, core.Request{
			Model:    model,
			Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
		})
		if sp, ok := p.(core.StreamProvider); ok {
			_, _ = sp.CompleteStream(ctx, core.Request{
				Model:    model,
				Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
			})
		}
		if ep, ok := p.(core.EmbeddingProvider); ok {
			_, _ = ep.Embed(ctx, core.EmbeddingRequest{Model: model, Input: "hi"})
		}
		if ip, ok := p.(core.ImageProvider); ok {
			_, _ = ip.GenerateImage(ctx, core.ImageRequest{Model: model, Prompt: "a cat"})
		}
	}
	if dp, ok := p.(core.DiscoveryProvider); ok {
		_, _ = dp.DiscoverModels(ctx)
	}
}

// TestBaseURLRootIsUsedVerbatim is the live half of the base-URL contract, and
// the half a source scan cannot reach: it asserts on the paths that actually
// leave the process.
//
// `<PROVIDER>_BASE_URL` is the API ROOT. Every surface therefore appends only
// its operation path to it, so a base mounted at /probe reaches /probe/<op> —
// never /probe/v1/<op>, which is what a provider that re-supplies its own
// version segment produces, and what silently doubles the segment for an
// operator who wrote the /v1 the vendor documents.
//
// The exceptions are named in hostRootProviders() (base_url_convention_test.go)
// with their reason, and are held to the same standard from the other side: an
// exception that stops appending a version segment fails there.
func TestBaseURLRootIsUsedVerbatim(t *testing.T) {
	rec := newPathRecorder(t)
	allowed := hostRootProviders()
	ctx := context.Background()

	checked := 0
	for _, entry := range providers.AllProviders() {
		cfg, ok := probeConfig(entry, rec.URL+rootProbe)
		if !ok {
			continue
		}
		t.Run(entry.ID, func(t *testing.T) {
			p, err := entry.Build(cfg)
			if err != nil {
				t.Fatalf("build %s: %v", entry.ID, err)
			}
			rec.drain()
			exerciseSurfaces(ctx, p)

			got := rec.drain()
			if len(got) == 0 {
				t.Fatalf("%s issued no request; the probe cannot see what it never sent", entry.ID)
			}
			for _, path := range got {
				rest, isProbe := strings.CutPrefix(path, rootProbe+"/")
				if !isProbe {
					t.Errorf("%s requested %q, which does not hang off the configured root %q; "+
						"the base URL is used verbatim", entry.ID, path, rootProbe)
					continue
				}
				if _, banned := allowed[strings.ReplaceAll(entry.ID, "-", "_")]; banned {
					continue
				}
				// Every segment, not just the head. A version segment reached by
				// way of a vendor prefix — "/openai/v1/chat/completions" — doubles
				// just as silently for an operator who wrote the /v1, and reading
				// only the first segment declares that path clean.
				for _, segment := range strings.Split(rest, "/") {
					if !isVersionSegment(segment) {
						continue
					}
					t.Errorf("%s requested %q: %q is a version segment the provider re-supplied on top of the "+
						"configured root. Fold it into the provider's default base instead, so the operator writes "+
						"one API root and every surface appends only its operation path.", entry.ID, path, segment)
					break
				}
			}
		})
		checked++
	}

	// A probe that silently exercised nothing is worse than no probe.
	if checked < 15 {
		t.Fatalf("probed only %d providers; probeConfig is not reaching the base-URL providers", checked)
	}
}

// probeConfig builds the minimal ProviderConfig that points entry at base,
// reporting false for a provider this probe cannot drive: one that takes no
// base URL at all, and one whose upstream is reached through a signed vendor
// SDK rather than the configured root (bedrock, vertex-ai), which is the same
// line test/conformance draws.
func probeConfig(entry providers.ProviderEntry, base string) (providers.ProviderConfig, bool) {
	cfg := providers.ProviderConfig{}
	baseKeys := 0
	for _, m := range entry.EnvMappings {
		switch m.ConfigKey {
		case providers.CfgKeyBaseURL, providers.CfgKeyHost:
			cfg[m.ConfigKey] = base
			baseKeys++
		case providers.CfgKeyAPIKey, providers.CfgKeyAPIToken:
			cfg[m.ConfigKey] = "probe-key"
		case providers.CfgKeyAccountID:
			cfg[m.ConfigKey] = "probe-account"
		case providers.CfgKeyDeployment:
			cfg[m.ConfigKey] = "probe-deployment"
		}
	}
	return cfg, baseKeys > 0
}

// isVersionSegment reports whether a path segment names an API version: v1, v2,
// v1beta.
//
// The trailing letters are optional but the shape is exact — "v", digits, then
// letters and nothing else — so a model id that happens to open the same way
// ("v1-5-pumpkin") is not mistaken for a version. This test reads every segment
// of an operation path, and a false positive there would ban a legitimate one.
func isVersionSegment(segment string) bool {
	return versionSegmentPattern.MatchString(segment)
}

var versionSegmentPattern = regexp.MustCompile(`^v[0-9]+[a-z]*$`)

// TestBareHostBaseURLAdoptsVersionSegment is the live guard for the one net in
// the base-URL contract, and the half TestBaseURLRootIsUsedVerbatim never
// exercises: its probe base carries a path (/probe), so the bare-host fallback
// in core.ResolveAPIRoot is dead code under it. Here the base is the recorder
// URL with NO path — a host, not an API root — so it must adopt the trailing
// version segment of the provider's own default root, and every request must
// hang off that segment. A provider that stores a bare host verbatim 404s every
// surface for the operator who forgot the /v1; this test fails instead.
//
// The expected segment per provider is read from that provider's
// defaultBaseURL constant, not guessed: "" means the default root ends in no
// version segment (perplexity is a bare host, deepinfra's root ends in
// /openai), so a bare-host base gains nothing and no request may start with a
// version segment. Providers absent from the map are skipped: some cannot be
// probed at all (bedrock, vertex-ai go through signed SDKs), and the
// hostRootProviders exceptions are outside the convention on purpose.
func TestBareHostBaseURLAdoptsVersionSegment(t *testing.T) {
	expectedSegment := map[string]string{
		"ai21":         "/v1",     // https://api.ai21.com/studio/v1
		"anthropic":    "/v1",     // https://api.anthropic.com/v1
		"cerebras":     "/v1",     // https://api.cerebras.ai/v1
		"cloudflare":   "/v1",     // https://api.cloudflare.com/client/v4/accounts/%s/ai/v1
		"deepinfra":    "",        // https://api.deepinfra.com/v1/openai — no trailing version segment
		"deepseek":     "/v1",     // https://api.deepseek.com/v1
		"fireworks":    "/v1",     // https://api.fireworks.ai/inference/v1
		"gemini":       "/v1beta", // https://generativelanguage.googleapis.com/v1beta
		"groq":         "/v1",     // https://api.groq.com/openai/v1
		"mistral":      "/v1",     // https://api.mistral.ai/v1
		"moonshot":     "/v1",     // https://api.moonshot.ai/v1
		"novita":       "/v1",     // https://api.novita.ai/openai/v1
		"nvidia-nim":   "/v1",     // https://integrate.api.nvidia.com/v1
		"ollama-cloud": "/v1",     // https://ollama.com/v1
		"openai":       "/v1",     // https://api.openai.com/v1
		"openrouter":   "/v1",     // https://openrouter.ai/api/v1
		"perplexity":   "",        // https://api.perplexity.ai — a bare host
		"qwen":         "/v1",     // https://dashscope-intl.aliyuncs.com/compatible-mode/v1
		"replicate":    "/v1",     // https://api.replicate.com/v1
		"sambanova":    "/v1",     // https://api.sambanova.ai/v1
		"together":     "/v1",     // https://api.together.ai/v1
		"xai":          "/v1",     // https://api.x.ai/v1
	}

	// siblingRoots names the one documented exception to "every path hangs off
	// the adopted segment": ollama-cloud serves embeddings on Ollama's native
	// API, the SIBLING of the OpenAI root (nativeRoot: <root>/v1 -> <root>/api),
	// so from a bare-host base its embed path is /api/embed by design.
	siblingRoots := map[string]string{
		"ollama-cloud": "/api/",
	}

	rec := newPathRecorder(t)
	ctx := context.Background()

	for _, entry := range providers.AllProviders() {
		want, ok := expectedSegment[entry.ID]
		if !ok {
			continue
		}
		cfg, probeable := probeConfig(entry, rec.URL) // NO path: the bare-host case
		if !probeable {
			t.Errorf("%s is in the expected-segment map but takes no base URL; remove it", entry.ID)
			continue
		}
		t.Run(entry.ID, func(t *testing.T) {
			p, err := entry.Build(cfg)
			if err != nil {
				t.Fatalf("build %s with a bare-host base: %v", entry.ID, err)
			}
			rec.drain()
			exerciseSurfaces(ctx, p)

			got := rec.drain()
			if len(got) == 0 {
				t.Fatalf("%s issued no request; the probe cannot see what it never sent", entry.ID)
			}
			for _, path := range got {
				if sibling, ok := siblingRoots[entry.ID]; ok && strings.HasPrefix(path, sibling) {
					continue
				}
				head := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 2)[0]
				switch {
				case want == "" && isVersionSegment(head):
					t.Errorf("%s requested %q from a bare-host base, but its default root ends in no "+
						"version segment, so nothing may be appended", entry.ID, path)
				case want != "" && !strings.HasPrefix(path, want+"/"):
					t.Errorf("%s requested %q from a bare-host base; a bare host adopts the default "+
						"root's version segment, so every path must start with %q", entry.ID, path, want+"/")
				}
			}
		})
	}
}
