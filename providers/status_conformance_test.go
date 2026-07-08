package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// statusConformanceCases covers every provider whose constructor accepts a
// base-URL override. Bedrock (AWS-SDK-signed transport) and Vertex AI
// (GCP-SDK auth) are intentionally excluded — neither takes a simple baseURL
// override, so they can't be pointed at a local stub without deeper
// credential/transport stubbing than this conformance test is worth.
func statusConformanceCases() []statusConformanceCase {
	return []statusConformanceCase{
		{name: "ai21", model: "jamba-mini-1.7", build: func(t *testing.T, baseURL string) Provider {
			t.Helper()
			p, err := ai21pkg.New(testAPIKey, baseURL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return p
		}},
		{name: "anthropic", build: func(t *testing.T, baseURL string) Provider {
			t.Helper()
			p, err := anthropicpkg.New(testAPIKey, baseURL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return p
		}},
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
		{name: "cerebras", build: func(t *testing.T, baseURL string) Provider {
			t.Helper()
			p, err := cerebraspkg.New(testAPIKey, baseURL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return p
		}},
		{name: "cloudflare", build: func(t *testing.T, baseURL string) Provider {
			t.Helper()
			p, err := cloudflarepkg.New(testAPIKey, "acct-123", baseURL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return p
		}},
		{name: "cohere", build: func(t *testing.T, baseURL string) Provider {
			t.Helper()
			p, err := coherepkg.New(testAPIKey, baseURL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return p
		}},
		{name: "databricks", build: func(t *testing.T, baseURL string) Provider {
			t.Helper()
			p, err := databrickspkg.New(testAPIKey, baseURL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return p
		}},
		{name: "deepinfra", build: func(t *testing.T, baseURL string) Provider {
			t.Helper()
			p, err := deepinfrapkg.New(testAPIKey, baseURL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return p
		}},
		{name: "deepseek", build: func(t *testing.T, baseURL string) Provider {
			t.Helper()
			p, err := deepseekpkg.New(testAPIKey, baseURL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return p
		}},
		{name: "fireworks", build: func(t *testing.T, baseURL string) Provider {
			t.Helper()
			p, err := fireworkspkg.New(testAPIKey, baseURL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return p
		}},
		{name: "gemini", build: func(t *testing.T, baseURL string) Provider {
			t.Helper()
			p, err := geminipkg.New(testAPIKey, baseURL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return p
		}},
		{name: "groq", build: func(t *testing.T, baseURL string) Provider {
			t.Helper()
			p, err := groqpkg.New(testAPIKey, baseURL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return p
		}},
		{name: "hugging_face", build: func(t *testing.T, baseURL string) Provider {
			t.Helper()
			p, err := huggingfacepkg.New(testAPIKey, baseURL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return p
		}},
		{name: "mistral", build: func(t *testing.T, baseURL string) Provider {
			t.Helper()
			p, err := mistralpkg.New(testAPIKey, baseURL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return p
		}},
		{name: "moonshot", build: func(t *testing.T, baseURL string) Provider {
			t.Helper()
			p, err := moonshotpkg.New(testAPIKey, baseURL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return p
		}},
		{name: "novita", build: func(t *testing.T, baseURL string) Provider {
			t.Helper()
			p, err := novitapkg.New(testAPIKey, baseURL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return p
		}},
		{name: "nvidia_nim", build: func(t *testing.T, baseURL string) Provider {
			t.Helper()
			p, err := nvidianimpkg.New(testAPIKey, baseURL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return p
		}},
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
		{name: "openai", build: func(t *testing.T, baseURL string) Provider {
			t.Helper()
			p, err := openaipkg.New(testAPIKey, baseURL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return p
		}},
		{name: "openrouter", build: func(t *testing.T, baseURL string) Provider {
			t.Helper()
			p, err := openrouterpkg.New(testAPIKey, baseURL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return p
		}},
		{name: "perplexity", build: func(t *testing.T, baseURL string) Provider {
			t.Helper()
			p, err := perplexitypkg.New(testAPIKey, baseURL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return p
		}},
		{name: "qwen", build: func(t *testing.T, baseURL string) Provider {
			t.Helper()
			p, err := qwenpkg.New(testAPIKey, baseURL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return p
		}},
		{name: "replicate", model: "test-owner/test-model", build: func(t *testing.T, baseURL string) Provider {
			t.Helper()
			p, err := replicatepkg.New(testAPIKey, baseURL, nil, nil)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return p
		}},
		{name: "sambanova", build: func(t *testing.T, baseURL string) Provider {
			t.Helper()
			p, err := sambanovapkg.New(testAPIKey, baseURL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return p
		}},
		{name: "together", build: func(t *testing.T, baseURL string) Provider {
			t.Helper()
			p, err := togetherpkg.New(testAPIKey, baseURL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return p
		}},
		{name: "xai", build: func(t *testing.T, baseURL string) Provider {
			t.Helper()
			p, err := xaipkg.New(testAPIKey, baseURL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return p
		}},
	}
}

// newStatusStub starts a stub HTTP server that always responds with status
// and an OpenAI-shaped {"error":{"message":…}} body, regardless of path.
func newStatusStub(status int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"message":"stub error"}}`))
	}))
}

// TestProviderStatusConformance verifies core.ParseStatusCode recovers the
// upstream HTTP status from every provider's Complete() (and, where
// implemented, CompleteStream()) error, for both a retryable (429) and a
// non-retryable (500) canned upstream response. Status-code recoverability is
// relied on by gateway_circuitbreaker.go's isRateLimitError and
// internal/strategies/fallback.go's onStatusCodes retry gate, so a provider
// that stops surfacing a parseable status breaks that gating silently. The
// streaming assertions only check that an error with a recoverable status is
// returned at all, not the message detail.
func TestProviderStatusConformance(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError} {
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
				if err == nil {
					t.Fatalf("Complete() returned no error for a %d upstream response", status)
				}
				if got := core.ParseStatusCode(err); got != status {
					t.Errorf("Complete(): ParseStatusCode(err) = %d, want %d; err = %v", got, status, err)
				}

				sp, ok := p.(StreamProvider)
				if !ok {
					return
				}
				ch, err := sp.CompleteStream(context.Background(), req)
				if err == nil {
					for range ch { //nolint:revive // drain to avoid a goroutine leak if a provider unexpectedly starts one
					}
					t.Fatalf("CompleteStream() returned no error for a %d upstream response", status)
				}
				if ch != nil {
					t.Errorf("CompleteStream() channel = %v, want nil on a pre-stream error", ch)
				}
				if got := core.ParseStatusCode(err); got != status {
					t.Errorf("CompleteStream(): ParseStatusCode(err) = %d, want %d; err = %v", got, status, err)
				}
			})
		}
	}
}
