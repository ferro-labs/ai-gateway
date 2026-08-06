package providers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ai21pkg "github.com/ferro-labs/ai-gateway/providers/ai21"
	anthropicpkg "github.com/ferro-labs/ai-gateway/providers/anthropic"
	azurefoundrypkg "github.com/ferro-labs/ai-gateway/providers/azure_foundry"
	azureopenaipkg "github.com/ferro-labs/ai-gateway/providers/azure_openai"
	cerebraspkg "github.com/ferro-labs/ai-gateway/providers/cerebras"
	cloudflarepkg "github.com/ferro-labs/ai-gateway/providers/cloudflare"
	coherepkg "github.com/ferro-labs/ai-gateway/providers/cohere"
	"github.com/ferro-labs/ai-gateway/providers/core"
	databrickspkg "github.com/ferro-labs/ai-gateway/providers/databricks"
	deepinfrapkg "github.com/ferro-labs/ai-gateway/providers/deepinfra"
	deepseekpkg "github.com/ferro-labs/ai-gateway/providers/deepseek"
	fireworkspkg "github.com/ferro-labs/ai-gateway/providers/fireworks"
	geminipkg "github.com/ferro-labs/ai-gateway/providers/gemini"
	groqpkg "github.com/ferro-labs/ai-gateway/providers/groq"
	huggingfacepkg "github.com/ferro-labs/ai-gateway/providers/hugging_face"
	mistralpkg "github.com/ferro-labs/ai-gateway/providers/mistral"
	moonshotpkg "github.com/ferro-labs/ai-gateway/providers/moonshot"
	novitapkg "github.com/ferro-labs/ai-gateway/providers/novita"
	nvidianimpkg "github.com/ferro-labs/ai-gateway/providers/nvidia_nim"
	ollamapkg "github.com/ferro-labs/ai-gateway/providers/ollama"
	ollamacloudpkg "github.com/ferro-labs/ai-gateway/providers/ollama_cloud"
	openaipkg "github.com/ferro-labs/ai-gateway/providers/openai"
	openrouterpkg "github.com/ferro-labs/ai-gateway/providers/openrouter"
	perplexitypkg "github.com/ferro-labs/ai-gateway/providers/perplexity"
	qwenpkg "github.com/ferro-labs/ai-gateway/providers/qwen"
	replicatepkg "github.com/ferro-labs/ai-gateway/providers/replicate"
	sambanovapkg "github.com/ferro-labs/ai-gateway/providers/sambanova"
	togetherpkg "github.com/ferro-labs/ai-gateway/providers/together"
	xaipkg "github.com/ferro-labs/ai-gateway/providers/xai"
)

// statusConformanceCase builds a provider pointed at a caller-supplied base
// URL, so it can be redirected to a local stub server.
type statusConformanceCase struct {
	name  string
	model string // model ID sent in the request; defaults to "test-model" if empty
	build func(t *testing.T, baseURL string) Provider
}

// simpleBuild adapts a provider constructor shaped func(apiKey, baseURL string)
// (P, error) — the shape shared by most providers — into a
// statusConformanceCase build func, so each such provider needs only a
// one-line case instead of repeating the same construct-and-check-error
// closure. Providers with a differently-shaped constructor (extra
// parameters, different argument order) still write their own closure below.
func simpleBuild[P Provider](newFn func(apiKey, baseURL string) (P, error)) func(t *testing.T, baseURL string) Provider {
	return func(t *testing.T, baseURL string) Provider {
		t.Helper()
		p, err := newFn(testAPIKey, baseURL)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return p
	}
}

// statusConformanceCases covers every provider whose constructor accepts a
// base-URL override. Bedrock (AWS-SDK-signed transport) and Vertex AI
// (GCP-SDK auth) are intentionally excluded — neither takes a simple baseURL
// override, so they can't be pointed at a local stub without deeper
// credential/transport stubbing than this conformance test is worth.
func statusConformanceCases() []statusConformanceCase {
	return []statusConformanceCase{
		{name: "ai21", model: "jamba-mini-1.7", build: simpleBuild(ai21pkg.New)},
		{name: "anthropic", build: simpleBuild(anthropicpkg.New)},
		{name: "azure_foundry", build: func(t *testing.T, baseURL string) Provider {
			t.Helper()
			p, err := azurefoundrypkg.New(testAPIKey, baseURL, "")
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return p
		}},
		{name: "azure_openai", build: func(t *testing.T, baseURL string) Provider {
			t.Helper()
			p, err := azureopenaipkg.New(testAPIKey, baseURL, "gpt-4o", "")
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return p
		}},
		{name: "cerebras", build: simpleBuild(cerebraspkg.New)},
		{name: "cloudflare", build: func(t *testing.T, baseURL string) Provider {
			t.Helper()
			p, err := cloudflarepkg.New(testAPIKey, "acct-123", baseURL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return p
		}},
		{name: "cohere", build: simpleBuild(coherepkg.New)},
		{name: "databricks", build: simpleBuild(databrickspkg.New)},
		{name: "deepinfra", build: simpleBuild(deepinfrapkg.New)},
		{name: "deepseek", build: simpleBuild(deepseekpkg.New)},
		{name: "fireworks", build: simpleBuild(fireworkspkg.New)},
		{name: "gemini", build: simpleBuild(geminipkg.New)},
		{name: "groq", build: simpleBuild(groqpkg.New)},
		{name: "hugging_face", build: simpleBuild(huggingfacepkg.New)},
		{name: "mistral", build: simpleBuild(mistralpkg.New)},
		{name: "moonshot", build: simpleBuild(moonshotpkg.New)},
		{name: "novita", build: simpleBuild(novitapkg.New)},
		{name: "nvidia_nim", build: simpleBuild(nvidianimpkg.New)},
		{name: "ollama", build: func(t *testing.T, baseURL string) Provider {
			t.Helper()
			p, err := ollamapkg.New(baseURL, nil)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return p
		}},
		{name: "ollama_cloud", build: func(t *testing.T, baseURL string) Provider {
			t.Helper()
			p, err := ollamacloudpkg.New(testAPIKey, baseURL, []string{"gpt-oss:20b"})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return p
		}},
		{name: "openai", build: simpleBuild(openaipkg.New)},
		{name: "openrouter", build: simpleBuild(openrouterpkg.New)},
		{name: "perplexity", build: simpleBuild(perplexitypkg.New)},
		{name: "qwen", build: simpleBuild(qwenpkg.New)},
		{name: "replicate", model: "test-owner/test-model", build: func(t *testing.T, baseURL string) Provider {
			t.Helper()
			p, err := replicatepkg.New(testAPIKey, baseURL, nil, nil)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return p
		}},
		{name: "sambanova", build: simpleBuild(sambanovapkg.New)},
		{name: "together", build: simpleBuild(togetherpkg.New)},
		{name: "xai", build: simpleBuild(xaipkg.New)},
	}
}

// statusStubLeakMarker is a value the stub puts in its error body OUTSIDE any
// recognised error envelope. Nothing reads it; assertTypedStatus asserts it
// never appears in the caller-facing message, which is how a provider that
// starts copying a raw upstream body into its error is caught.
const statusStubLeakMarker = "UNSTRUCTURED-BODY-MUST-NOT-LEAK"

// newStatusStub starts a stub HTTP server that always responds with status
// and an OpenAI-shaped {"error":{"message":…}} body, regardless of path.
func newStatusStub(status int) *httptest.Server {
	body := fmt.Sprintf(`{"error":{"message":"stub error"},"trace":%q}`, statusStubLeakMarker)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

// assertTypedStatus is the whole provider→gateway error contract, asserted in
// one place. Every provider, on every surface, must satisfy it for an upstream
// that answered with a non-success status.
//
// It checks the type, not the text. A message that merely reads like it carries
// a status is what the deleted regex in core.ParseStatusCode used to accept, and
// accepting it is what made an SDK-backed call — whose message has no such
// shape — silently classify as a transport failure.
func assertTypedStatus(t *testing.T, surface string, err error, wantStatus int) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: no error for a %d upstream response", surface, wantStatus)
	}
	var statusErr *core.HTTPStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("%s: errors.As found no *core.HTTPStatusError in %T: %v", surface, err, err)
	}
	if statusErr.StatusCode != wantStatus {
		t.Errorf("%s: StatusCode = %d, want %d; err = %v", surface, statusErr.StatusCode, wantStatus, err)
	}
	if statusErr.Provider == "" {
		t.Errorf("%s: Provider is empty; the operator-facing line cannot name the backend", surface)
	}
	if statusErr.Message == "" {
		t.Errorf("%s: Message is empty; a caller-facing 400/422 would say nothing", surface)
	}
	// The upstream stub answers with a body containing this marker in no
	// recognised error envelope. It must not reach the caller-facing message.
	if strings.Contains(statusErr.Message, statusStubLeakMarker) {
		t.Errorf("%s: Message = %q — an unrecognised upstream body reached the caller-facing text", surface, statusErr.Message)
	}
	// ParseStatusCode is the accessor everything downstream uses; it must agree.
	if got := core.ParseStatusCode(err); got != wantStatus {
		t.Errorf("%s: ParseStatusCode = %d, want %d", surface, got, wantStatus)
	}
}

// TestProviderStatusConformance is the contract test for stream S1: every
// provider returns a *core.HTTPStatusError (or an error wrapping one) at its
// boundary, on every surface it implements, across the status classes an
// upstream actually returns — client-auth (401), not-found (404), retryable
// rate-limit (429) and non-retryable server fault (500).
//
// It is what a THIRTY-FIRST provider inherits without writing anything: add a
// case to statusConformanceCases and the contract is enforced for chat,
// streaming, embeddings and images at once. It is also what stops the contract
// from rotting — internal/strategies.shouldRetry, gateway_circuitbreaker's
// isRateLimitError and internal/apierror's classification all read the status
// off this type, and all three fail open (retry it, blame the provider, answer
// 500) when it is missing, which is silent in production and loud only here.
func TestProviderStatusConformance(t *testing.T) {
	for _, status := range []int{
		http.StatusUnauthorized,        // 401 — bad key
		http.StatusNotFound,            // 404 — unknown model / route
		http.StatusTooManyRequests,     // 429 — retryable
		http.StatusInternalServerError, // 500 — non-retryable fault
	} {
		for _, tc := range statusConformanceCases() {
			t.Run(fmt.Sprintf("%s/%d", tc.name, status), func(t *testing.T) {
				srv := newStatusStub(status)
				defer srv.Close()

				model := tc.model
				if model == "" {
					model = "test-model"
				}
				req := core.Request{
					Model:    model,
					Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
				}

				p := tc.build(t, srv.URL)
				_, err := p.Complete(context.Background(), req)
				assertTypedStatus(t, "Complete", err, status)

				if sp, ok := p.(StreamProvider); ok {
					ch, streamErr := sp.CompleteStream(context.Background(), req)
					if streamErr == nil {
						for range ch { //nolint:revive // drain to avoid a goroutine leak if a provider unexpectedly starts one
						}
						t.Fatalf("CompleteStream() returned no error for a %d upstream response", status)
					}
					if ch != nil {
						t.Errorf("CompleteStream() channel = %v, want nil on a pre-stream error", ch)
					}
					assertTypedStatus(t, "CompleteStream", streamErr, status)
				}

				// Embeddings and images are where the contract was broken: they
				// are the surfaces reached through an SDK rather than raw HTTP,
				// and an SDK's own error type carries its status in a field this
				// gateway cannot see until a provider translates it.
				if ep, ok := p.(EmbeddingProvider); ok {
					_, embedErr := ep.Embed(context.Background(), core.EmbeddingRequest{
						Model: embedModelFor(tc.name, model),
						Input: "hi",
					})
					assertTypedStatus(t, "Embed", embedErr, status)
				}
				if ip, ok := p.(ImageProvider); ok {
					_, imageErr := ip.GenerateImage(context.Background(), core.ImageRequest{
						Model:  imageModelFor(tc.name, model),
						Prompt: "a cat",
					})
					assertTypedStatus(t, "GenerateImage", imageErr, status)
				}
			})
		}
	}
}

// embedModelFor and imageModelFor supply a model id for the providers that
// route on it. The stub answers any path with the canned status, so the id only
// has to survive the provider's own pre-flight validation.
func embedModelFor(provider, fallback string) string {
	switch provider {
	case "cohere":
		return "embed-english-v3.0"
	case "openai":
		return "text-embedding-3-small"
	default:
		return fallback
	}
}

func imageModelFor(provider, fallback string) string {
	switch provider {
	case "openai":
		return "dall-e-3"
	default:
		return fallback
	}
}
