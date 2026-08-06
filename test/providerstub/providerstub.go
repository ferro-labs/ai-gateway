// Package providerstub builds any registered provider against an in-memory HTTP
// stub — no socket, no network — for benchmarks and fuzzing.
//
// Each provider constructs its own *http.Client via the shared providerhttp
// manager, which caches one client per provider name. Build wires the provider
// at a dummy base URL; InstallResponse then swaps a mock http.RoundTripper onto
// that cached client so Complete answers from memory. The single fixture table
// here is the one source both test/bench and test/fuzz draw from, guarded by
// providerstub_test.go so it cannot drift from the registry.
package providerstub

import (
	"bytes"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

const (
	// StubAPIKey is the credential every stubbed provider is built with.
	StubAPIKey = "stub-key"
	// StubBaseURL is any valid absolute root; the mock transport ignores the host
	// and path, so no request ever leaves the process.
	StubBaseURL = "http://stub.local"
)

// Fixture is what a provider's ProviderEntry needs beyond the base URL. It
// mirrors test/conformance's fixture table — the model is one the provider's
// SupportsModel accepts, so Complete exercises the real translation path.
type Fixture struct {
	Model      string
	ExtraCfg   providers.ProviderConfig
	BaseURLKey string // empty ⇒ CfgKeyBaseURL; Ollama reads CfgKeyHost
}

// Fixtures maps every stubbable provider to the config that builds it.
func Fixtures() map[string]Fixture {
	return map[string]Fixture{
		providers.NameOpenAI:    {Model: "gpt-4o-mini"},
		providers.NameAnthropic: {Model: "claude-sonnet-4-6"},
		providers.NameGemini:    {Model: "gemini-2.5-flash"},
		providers.NameCohere:    {Model: "command-r"},
		providers.NameReplicate: {
			Model: "meta/meta-llama-3-8b-instruct",
			ExtraCfg: providers.ProviderConfig{
				providers.CfgKeyAPIToken:   StubAPIKey,
				providers.CfgKeyTextModels: "meta/meta-llama-3-8b-instruct",
			},
		},
		providers.NameAI21:         {Model: "jamba-mini-1.7"},
		providers.NameMistral:      {Model: "mistral-small-latest"},
		providers.NameDeepSeek:     {Model: "deepseek-chat"},
		providers.NameGroq:         {Model: "llama-3.1-8b-instant"},
		providers.NameAzureFoundry: {Model: "gpt-4o"},
		providers.NameAzureOpenAI: {
			Model:    "gpt-4o-mini",
			ExtraCfg: providers.ProviderConfig{providers.CfgKeyDeployment: "gpt-4o-mini"},
		},
		providers.NameCerebras: {Model: "llama-3.3-70b"},
		providers.NameCloudflare: {
			Model:    "@cf/meta/llama-3.1-8b-instruct",
			ExtraCfg: providers.ProviderConfig{providers.CfgKeyAccountID: "stub-account"},
		},
		providers.NameDatabricks:  {Model: "databricks-claude-sonnet-4-5"},
		providers.NameDeepInfra:   {Model: "deepseek-ai/DeepSeek-R1"},
		providers.NameFireworks:   {Model: "accounts/fireworks/models/llama-v3p1-8b-instruct"},
		providers.NameHuggingFace: {Model: "meta-llama/Meta-Llama-3.1-8B-Instruct"},
		providers.NameMoonshot:    {Model: "kimi-k2.5"},
		providers.NameNovita:      {Model: "deepseek/deepseek-v3.2"},
		providers.NameNVIDIANIM:   {Model: "meta/llama-3.1-8b-instruct"},
		providers.NameOllama:      {Model: "llama3.2", BaseURLKey: providers.CfgKeyHost},
		providers.NameOllamaCloud: {Model: "gpt-oss:120b"},
		providers.NameOpenRouter:  {Model: "openrouter/auto"},
		providers.NamePerplexity:  {Model: "sonar"},
		providers.NameQwen:        {Model: "qwen-turbo"},
		providers.NameSambaNova:   {Model: "Meta-Llama-3.1-8B-Instruct"},
		providers.NameTogether:    {Model: "meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo"},
		providers.NameXAI:         {Model: "grok-2-latest"},
	}
}

// Uncovered lists providers that cannot be stubbed this way, with the reason:
// Bedrock and Vertex AI use an SDK transport, not the shared providerhttp client.
func Uncovered() map[string]string {
	return map[string]string{
		providers.NameBedrock:  "AWS SDK transport (SigV4); does not use the shared providerhttp client",
		providers.NameVertexAI: "GCP OAuth SDK transport; does not use the shared providerhttp client",
	}
}

// Build constructs a provider at StubBaseURL. It fails the test if the provider
// is unknown or its ProviderEntry rejects the config.
func Build(tb testing.TB, id string) core.Provider {
	tb.Helper()
	fx, ok := Fixtures()[id]
	if !ok {
		tb.Fatalf("providerstub: no fixture for %q", id)
	}
	entry, ok := providers.GetProviderEntry(id)
	if !ok {
		tb.Fatalf("providerstub: no ProviderEntry for %q", id)
	}
	key := fx.BaseURLKey
	if key == "" {
		key = providers.CfgKeyBaseURL
	}
	cfg := providers.ProviderConfig{providers.CfgKeyAPIKey: StubAPIKey, key: StubBaseURL}
	maps.Copy(cfg, fx.ExtraCfg)
	p, err := entry.Build(cfg)
	if err != nil {
		tb.Fatalf("providerstub: build %q: %v", id, err)
	}
	return p
}

// mockTransport answers every request in memory with a fixed status and body —
// no dialing, no socket. It drains the request body first so the provider's full
// request marshalling is exercised.
type mockTransport struct {
	status int
	body   []byte
}

func (m mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
		_ = req.Body.Close()
	}
	return &http.Response{
		StatusCode:    m.status,
		Status:        http.StatusText(m.status),
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(bytes.NewReader(m.body)),
		ContentLength: int64(len(m.body)),
		Request:       req,
	}, nil
}

// InstallResponse swaps a mock transport returning (status, body) onto the shared
// client the provider uses, and returns a restore func. Callers run serially, so
// mutating the process-shared client and restoring it after is safe.
func InstallResponse(id string, status int, body []byte) (restore func()) {
	client := providerhttp.ForProvider(id)
	orig := client.Transport
	client.Transport = mockTransport{status: status, body: body}
	return func() { client.Transport = orig }
}

// LoadChatResponse reads a provider's native success payload from its testdata.
// The directory is the provider ID with dashes as underscores (nvidia-nim →
// nvidia_nim).
func LoadChatResponse(tb testing.TB, id string) []byte {
	tb.Helper()
	dir := strings.ReplaceAll(id, "-", "_")
	path := filepath.Join(repoRoot(), "providers", dir, "testdata", "chat.response.json")
	data, err := os.ReadFile(path) //nolint:gosec // fixed in-repo testdata path
	if err != nil {
		tb.Fatalf("providerstub: read %s chat.response.json: %v", id, err)
	}
	return data
}

// ChatRequest is a small, valid request every stubbed provider accepts.
func ChatRequest(model string) core.Request {
	maxTokens := 128
	temperature := 0.7
	return core.Request{
		Model: model,
		Messages: []core.Message{
			{Role: core.RoleSystem, Content: "You are a helpful assistant."},
			{Role: core.RoleUser, Content: "Summarize the latest deployment status in one sentence."},
		},
		MaxTokens:   &maxTokens,
		Temperature: &temperature,
	}
}

// repoRoot resolves the module root from this file's location (test/providerstub).
func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}
