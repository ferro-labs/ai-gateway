//go:build live

package live_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/config"
	adminmodel "github.com/ferro-labs/ai-gateway/internal/admin/model"
	"github.com/ferro-labs/ai-gateway/internal/admin/repository"
	"github.com/ferro-labs/ai-gateway/internal/httpserver"
	"github.com/ferro-labs/ai-gateway/internal/requestlog"
	"github.com/ferro-labs/ai-gateway/mcp"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/pkg/metrics"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"

	_ "github.com/ferro-labs/ai-gateway/plugin/budget"
	_ "github.com/ferro-labs/ai-gateway/plugin/cache"
	_ "github.com/ferro-labs/ai-gateway/plugin/maxtoken"
	_ "github.com/ferro-labs/ai-gateway/plugin/ratelimit"
	_ "github.com/ferro-labs/ai-gateway/plugin/wordfilter"
)

const (
	goal21ProviderID = "openai"
	goal21Model      = "gpt-4o-mini"
)

type goal21CountingProvider struct {
	delegate       core.Provider
	forceFirstTool bool
	calls          atomic.Int64
	mu             sync.Mutex
	requests       []core.Request
}

func (p *goal21CountingProvider) Name() string { return p.delegate.Name() }

func (p *goal21CountingProvider) SupportsModel(model string) bool {
	return p.delegate.SupportsModel(model)
}

func (p *goal21CountingProvider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	call := p.calls.Add(1)
	if p.forceFirstTool {
		if call == 1 {
			req.ToolChoice = map[string]any{
				"type":     "function",
				"function": map[string]any{"name": "get_answer"},
			}
		} else {
			req.ToolChoice = nil
		}
	}
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	return p.delegate.Complete(ctx, req)
}

func (p *goal21CountingProvider) requestSnapshot() []core.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]core.Request(nil), p.requests...)
}

type goal21FailProvider struct {
	calls atomic.Int64
}

func (*goal21FailProvider) Name() string              { return "cohere" }
func (*goal21FailProvider) SupportsModel(string) bool { return true }
func (p *goal21FailProvider) Complete(context.Context, core.Request) (*core.Response, error) {
	p.calls.Add(1)
	return nil, core.StatusError(p.Name(), http.StatusServiceUnavailable, "controlled local failure")
}

func goal21LiveProvider(t *testing.T) core.Provider {
	t.Helper()
	entry, ok := providers.GetProviderEntry(goal21ProviderID)
	if !ok {
		t.Fatal("Goal 21 provider entry is unavailable")
	}
	cfg := providers.ProviderConfigFromEnv(entry)
	if cfg == nil {
		t.Fatal("Goal 21 provider credential set is incomplete")
	}
	p, err := entry.Build(cfg)
	if err != nil {
		t.Fatalf("construct Goal 21 provider: %s", liveErrorClass(err))
	}
	return p
}

func goal21PluginConfig() []config.PluginConfig {
	loggerConfig := map[string]any{"persist": true, "level": "info"}
	budgetConfig := map[string]any{
		"store_id":            "goal21-combined",
		"spend_limit_usd":     0.000000001,
		"input_per_m_tokens":  0.15,
		"output_per_m_tokens": 0.60,
	}
	return []config.PluginConfig{
		{
			Name: "word-filter", Type: "guardrail", Stage: "before_request", Enabled: true,
			Config: map[string]any{"blocked_words": []any{"goal21-blocked"}, "case_sensitive": false},
		},
		{
			Name: "max-token", Type: "guardrail", Stage: "before_request", Enabled: true,
			Config: map[string]any{"max_tokens": 16, "max_messages": 10},
		},
		{
			Name: "rate-limit", Type: "ratelimit", Stage: "before_request", Enabled: true,
			Config: map[string]any{"requests_per_second": 100, "burst": 100, "key_rpm": 100},
		},
		{Name: "budget", Type: "ratelimit", Stage: "before_request", Enabled: true, Config: budgetConfig},
		{Name: "request-logger", Type: "logging", Stage: "before_request", Enabled: true, Config: loggerConfig},
		{Name: "budget", Type: "ratelimit", Stage: "after_request", Enabled: true, Config: budgetConfig},
		{Name: "request-logger", Type: "logging", Stage: "after_request", Enabled: true, Config: loggerConfig},
		{Name: "request-logger", Type: "logging", Stage: "on_error", Enabled: true, Config: loggerConfig},
	}
}

func goal21CachePluginConfig() []config.PluginConfig {
	cacheConfig := map[string]any{"max_age": 300, "max_entries": 10}
	loggerConfig := map[string]any{"persist": true, "level": "info"}
	return []config.PluginConfig{
		{Name: "response-cache", Type: "transform", Stage: "before_request", Enabled: true, Config: cacheConfig},
		{Name: "request-logger", Type: "logging", Stage: "before_request", Enabled: true, Config: loggerConfig},
		{Name: "response-cache", Type: "transform", Stage: "after_request", Enabled: true, Config: cacheConfig},
		{Name: "request-logger", Type: "logging", Stage: "after_request", Enabled: true, Config: loggerConfig},
		{Name: "request-logger", Type: "logging", Stage: "on_error", Enabled: true, Config: loggerConfig},
	}
}

type goal21HTTPEnv struct {
	server   *httptest.Server
	gw       *aigateway.Gateway
	logs     *requestlog.SQLWriter
	observer *goal20Observer
	keys     repository.Store
	audit    repository.AuditStore
	adminKey *adminmodel.APIKey
	provider *goal21CountingProvider
}

func newGoal21HTTPEnv(t *testing.T, plugins []config.PluginConfig) *goal21HTTPEnv {
	t.Helper()
	t.Setenv("ALLOW_UNAUTHENTICATED_PROXY", "false")
	t.Setenv(models.CatalogFetchTimeoutEnv, "0")

	counted := &goal21CountingProvider{delegate: goal21LiveProvider(t)}
	gw, err := aigateway.New(config.Config{
		Strategy:       config.StrategyConfig{Mode: config.ModeSingle},
		RequestTimeout: "60s",
		Targets: []config.Target{{
			VirtualKey: goal21ProviderID,
			Models:     []string{goal21Model},
			Retry:      &config.RetryConfig{Attempts: 1},
		}},
		Plugins: plugins,
	})
	if err != nil {
		t.Fatalf("construct Goal 21 gateway: %v", err)
	}
	t.Cleanup(func() {
		if err := gw.Close(); err != nil {
			t.Errorf("close Goal 21 gateway: %v", err)
		}
	})

	logs, err := requestlog.NewSQLiteWriter(t.Context(), filepath.Join(t.TempDir(), "requests.db"))
	if err != nil {
		t.Fatalf("create Goal 21 request-log store: %T", err)
	}
	t.Cleanup(func() {
		if err := logs.Close(); err != nil {
			t.Errorf("close Goal 21 request-log store: %v", err)
		}
	})
	gw.SetRequestLogWriter(logs)
	gw.RegisterProvider(counted)
	observer := newGoal20Observer()
	gw.SetObservability(observer)
	if err := gw.LoadPlugins(); err != nil {
		t.Fatalf("load Goal 21 plugins: %v", err)
	}

	keys := repository.NewKeyStore()
	adminKey, err := keys.Create(t.Context(), "goal21-admin", []string{adminmodel.ScopeAdmin}, nil)
	if err != nil {
		t.Fatalf("create Goal 21 admin key: %T", err)
	}
	audit := repository.NewMemoryAuditStore()
	registry := providers.NewRegistry()
	registry.Register(counted)
	router := httpserver.NewRouter(
		registry,
		keys,
		repository.NewSessionStore(),
		nil,
		gw,
		nil,
		nil,
		logs,
		logs,
		audit,
		"",
		nil,
	)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return &goal21HTTPEnv{
		server: server, gw: gw, logs: logs, observer: observer, keys: keys,
		audit: audit, adminKey: adminKey, provider: counted,
	}
}

func goal21PostJSON(t *testing.T, url, key string, payload map[string]any) *http.Response {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode Goal 21 request: %T", err)
	}
	return goal20Do(t, http.MethodPost, url, key, bytes.NewReader(body))
}

func goal21WaitForLog(t *testing.T, store *requestlog.SQLWriter, traceID string) requestlog.Entry {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		result, err := store.List(t.Context(), requestlog.Query{Stages: requestlog.TerminalStages()})
		if err != nil {
			t.Fatalf("read Goal 21 request log: %T", err)
		}
		for _, entry := range result.Data {
			if entry.TraceID == traceID {
				return entry
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("Goal 21 request log did not correlate to X-Request-ID")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func goal21WaitForCompletedEvent(t *testing.T, observer *goal20Observer, traceID, provider string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		observer.mu.Lock()
		found := false
		for _, event := range observer.events {
			if event.TraceID == traceID && event.Subject == aigateway.SubjectRequestCompleted &&
				event.Provider == provider && event.Status == http.StatusOK {
				found = true
				break
			}
		}
		observer.mu.Unlock()
		if found {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Goal 21 completion event did not correlate to X-Request-ID")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func goal21ReadChat(t *testing.T, body io.ReadCloser) {
	t.Helper()
	defer func() { _ = body.Close() }()
	var response core.Response
	if err := json.NewDecoder(body).Decode(&response); err != nil {
		t.Fatalf("decode Goal 21 response: %T", err)
	}
	if response.Provider != goal21ProviderID || len(response.Choices) == 0 {
		t.Fatal("Goal 21 response shape or provider attribution mismatch")
	}
	message := response.Choices[0].Message
	if message.Content == "" && message.ReasoningContent == "" && len(message.ToolCalls) == 0 {
		t.Fatal("Goal 21 response has no normalized output")
	}
}

func TestLiveGoal21_AuthenticatedPoliciesAttributionAndSignals(t *testing.T) {
	env := newGoal21HTTPEnv(t, goal21PluginConfig())

	ready := goal20Do(t, http.MethodGet, env.server.URL+"/readyz", "", nil) //nolint:bodyclose // body is closed via the goal20Close helper, which bodyclose cannot follow
	goal20RequireStatus(t, ready, http.StatusOK)
	goal20Close(t, ready.Body)
	for _, path := range []string{"/v1/models", "/v1/capabilities"} {
		resp := goal20Do(t, http.MethodGet, env.server.URL+path, env.adminKey.Key, nil) //nolint:bodyclose // body is closed via the goal20Close helper, which bodyclose cannot follow
		goal20RequireStatus(t, resp, http.StatusOK)
		goal20Close(t, resp.Body)
	}

	createdResp := goal21PostJSON(t, env.server.URL+"/admin/keys", env.adminKey.Key, map[string]any{
		"name": "goal21-live-client", "scopes": []string{adminmodel.ScopeReadOnly},
	})
	goal20RequireStatus(t, createdResp, http.StatusCreated)
	var clientKey adminmodel.APIKey
	if err := json.NewDecoder(createdResp.Body).Decode(&clientKey); err != nil {
		t.Fatalf("decode created Goal 21 key: %T", err)
	}
	_ = createdResp.Body.Close()
	if clientKey.ID == "" || clientKey.Key == "" {
		t.Fatal("admin key creation returned an incomplete credential")
	}

	for _, tc := range []struct {
		prompt    string
		maxTokens int
		status    int
	}{
		{prompt: "goal21-blocked", maxTokens: 16, status: http.StatusBadRequest},
		{prompt: "Say exactly: pong", maxTokens: 17, status: http.StatusBadRequest},
	} {
		resp := goal21PostJSON(t, env.server.URL+"/v1/chat/completions", clientKey.Key, map[string]any{ //nolint:bodyclose // body is closed via the goal20Close helper, which bodyclose cannot follow
			"model": goal21Model, "messages": []map[string]string{{"role": "user", "content": tc.prompt}},
			"max_tokens": tc.maxTokens,
		})
		goal20RequireStatus(t, resp, tc.status)
		goal20Close(t, resp.Body)
	}
	if env.provider.calls.Load() != 0 {
		t.Fatal("a rejected policy request reached the live provider")
	}

	handles := metrics.ForRequest(goal21ProviderID, goal21Model)
	successBefore := goal20CounterValue(t, handles.Success)
	liveResp := goal21PostJSON(t, env.server.URL+"/v1/chat/completions", clientKey.Key, map[string]any{ //nolint:bodyclose // body is closed via the goal21ReadChat helper, which bodyclose cannot follow
		"model":      goal21Model,
		"messages":   []map[string]string{{"role": "user", "content": "Say exactly: pong"}},
		"max_tokens": 16,
	})
	goal20RequireStatus(t, liveResp, http.StatusOK)
	traceID := goal20RequestID(t, liveResp)
	goal21ReadChat(t, liveResp.Body)
	entry := goal21WaitForLog(t, env.logs, traceID)
	if entry.APIKeyID != clientKey.ID || entry.Provider != goal21ProviderID || entry.TotalTokens <= 0 {
		t.Fatal("live request log is missing credential, provider, or usage attribution")
	}
	metricModel := goal21Model
	metricDelta := goal20CounterValue(t, handles.Success) - successBefore
	if metricDelta != 1 && entry.Model != "" && entry.Model != goal21Model {
		metricModel = entry.Model
		metricDelta = goal20CounterValue(t, metrics.ForRequest(goal21ProviderID, metricModel).Success)
	}
	if metricDelta != 1 {
		t.Fatalf("live success metric delta=%v, want 1", metricDelta)
	}
	goal21WaitForCompletedEvent(t, env.observer, traceID, goal21ProviderID)

	budgetResp := goal21PostJSON(t, env.server.URL+"/v1/chat/completions", clientKey.Key, map[string]any{ //nolint:bodyclose // body is closed via the goal20Close helper, which bodyclose cannot follow
		"model":      goal21Model,
		"messages":   []map[string]string{{"role": "user", "content": "Say exactly: second"}},
		"max_tokens": 16,
	})
	goal20RequireStatus(t, budgetResp, http.StatusPaymentRequired)
	budgetTraceID := goal20RequestID(t, budgetResp)
	goal20Close(t, budgetResp.Body)
	budgetEntry := goal21WaitForLog(t, env.logs, budgetTraceID)
	if budgetEntry.Stage != "on_error" || budgetEntry.APIKeyID != clientKey.ID {
		t.Fatal("budget rejection was not recorded against the live client key")
	}
	if env.provider.calls.Load() != 1 {
		t.Fatalf("combined policy journey made %d live calls, want 1", env.provider.calls.Load())
	}

	stats, err := env.logs.Stats(t.Context(), requestlog.Query{APIKeyID: &clientKey.ID})
	if err != nil {
		t.Fatalf("read Goal 21 key stats: %T", err)
	}
	if stats.TotalEntries < 4 || stats.ByProvider[goal21ProviderID].Count != 1 {
		t.Fatal("request-log stats do not attribute the combined journey")
	}
	auditRows, err := env.audit.List(t.Context(), adminmodel.AuditQuery{
		Action: "key.create", ActorID: env.adminKey.ID,
	})
	if err != nil {
		t.Fatalf("read Goal 21 audit trail: %T", err)
	}
	if auditRows.Total != 1 || auditRows.Data[0].TargetID != clientKey.ID ||
		auditRows.Data[0].Outcome != adminmodel.AuditOK {
		t.Fatal("key creation audit attribution mismatch")
	}
	t.Logf("scenarios=S01,S04,S06,S07,S10 live_calls=1 trace_correlated=true metric_model=%s terminal_rows=%d",
		metricModel, stats.TotalEntries)
}

func TestLiveGoal21_ControlledFailureFallsBackToLive(t *testing.T) {
	t.Setenv(models.CatalogFetchTimeoutEnv, "0")
	live := &goal21CountingProvider{delegate: goal21LiveProvider(t)}
	failing := &goal21FailProvider{}
	gw, err := aigateway.New(config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets: []config.Target{
			{VirtualKey: failing.Name(), Models: []string{goal21Model}, Retry: &config.RetryConfig{Attempts: 1}},
			{VirtualKey: live.Name(), Models: []string{goal21Model}, Retry: &config.RetryConfig{Attempts: 1}},
		},
		RequestTimeout: "60s",
	})
	if err != nil {
		t.Fatalf("construct fallback gateway: %v", err)
	}
	t.Cleanup(func() { _ = gw.Close() })
	gw.RegisterProvider(failing)
	gw.RegisterProvider(live)
	observer := newGoal20Observer()
	gw.SetObservability(observer)

	maxTokens := 16
	response, err := gw.Route(t.Context(), core.Request{
		Model:     goal21Model,
		Messages:  []core.Message{{Role: core.RoleUser, Content: "Say exactly: pong"}},
		MaxTokens: &maxTokens,
	})
	if err != nil {
		t.Fatalf("fallback route failed: %s", liveErrorClass(err))
	}
	if response.Provider != goal21ProviderID || len(response.Choices) == 0 {
		t.Fatal("fallback did not return the live provider response")
	}
	if failing.calls.Load() != 1 || live.calls.Load() != 1 {
		t.Fatalf("fallback calls local=%d live=%d, want 1 each", failing.calls.Load(), live.calls.Load())
	}
	observer.mu.Lock()
	if len(observer.starts) != 1 {
		observer.mu.Unlock()
		t.Fatal("fallback route did not open exactly one request span")
	}
	traceID := observer.starts[0].TraceID
	observer.mu.Unlock()
	goal21WaitForCompletedEvent(t, observer, traceID, goal21ProviderID)
	t.Log("scenario=S03 controlled_failures=1 live_calls=1 retries=0 completed_event_correlated=true")
}

func TestLiveGoal21_ResponseCacheSkipsSecondLiveCall(t *testing.T) {
	env := newGoal21HTTPEnv(t, goal21CachePluginConfig())
	payload := map[string]any{
		"model":      goal21Model,
		"messages":   []map[string]string{{"role": "user", "content": "Say exactly: cache"}},
		"max_tokens": 16,
	}
	entries := make([]requestlog.Entry, 0, 2)
	for range 2 {
		resp := goal21PostJSON(t, env.server.URL+"/v1/chat/completions", env.adminKey.Key, payload) //nolint:bodyclose // body is closed via the goal21ReadChat helper, which bodyclose cannot follow
		goal20RequireStatus(t, resp, http.StatusOK)
		traceID := goal20RequestID(t, resp)
		goal21ReadChat(t, resp.Body)
		entries = append(entries, goal21WaitForLog(t, env.logs, traceID))
	}
	if env.provider.calls.Load() != 1 {
		t.Fatalf("identical requests made %d live calls, want 1", env.provider.calls.Load())
	}
	if entries[0].CostUSD == nil || *entries[0].CostUSD <= 0 {
		t.Fatal("cache-prime request is missing provider cost")
	}
	if entries[1].CostUSD == nil || *entries[1].CostUSD != 0 {
		t.Fatal("cache-served request did not record known zero incremental cost")
	}
	if entries[0].APIKeyID != env.adminKey.ID || entries[1].APIKeyID != env.adminKey.ID {
		t.Fatal("cache journey lost API-key attribution")
	}
	t.Log("scenario=S05 client_requests=2 live_calls=1 cache_hit_cost_usd=0 attributed=true")
}

func newGoal21MCPServer(t *testing.T, calls *atomic.Int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		defer func() { _ = r.Body.Close() }()
		var rpcReq struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&rpcReq); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		write := func(result any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": rpcReq.ID, "result": result,
			})
		}
		switch rpcReq.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "goal21-session")
			write(map[string]any{
				"name": "goal21-mcp", "version": "1.0", "protocolVersion": "2025-03-26",
				"capabilities": map[string]any{"tools": map[string]any{}},
			})
		case "tools/list":
			write(map[string]any{"tools": []map[string]any{{
				"name": "get_answer", "description": "Returns a fixed answer.",
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"q": map[string]any{"type": "string"}},
				},
			}}})
		case "tools/call":
			calls.Add(1)
			write(map[string]any{
				"content": []map[string]any{{"type": "text", "text": "42"}},
				"isError": false,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestLiveGoal21_DeterministicMCPToolLoop(t *testing.T) {
	t.Setenv(models.CatalogFetchTimeoutEnv, "0")
	var toolCalls atomic.Int64
	mcpServer := newGoal21MCPServer(t, &toolCalls)
	t.Cleanup(mcpServer.Close)

	live := &goal21CountingProvider{delegate: goal21LiveProvider(t), forceFirstTool: true}
	gw, err := aigateway.New(config.Config{
		Strategy:       config.StrategyConfig{Mode: config.ModeSingle},
		RequestTimeout: "60s",
		Targets: []config.Target{{
			VirtualKey: goal21ProviderID,
			Models:     []string{goal21Model},
			Retry:      &config.RetryConfig{Attempts: 1},
		}},
		MCPServers: []mcp.ServerConfig{{
			Name: "goal21-mcp", URL: mcpServer.URL + "/mcp", TimeoutSeconds: 5, Required: true,
		}},
	})
	if err != nil {
		t.Fatalf("construct MCP gateway: %v", err)
	}
	t.Cleanup(func() { _ = gw.Close() })
	gw.RegisterProvider(live)
	select {
	case <-gw.MCPInitDone():
	case <-time.After(10 * time.Second):
		t.Fatal("Goal 21 MCP initialization timed out")
	}
	if readiness := gw.Readiness(); !readiness.Ready || len(readiness.MCPServers) != 1 || !readiness.MCPServers[0].Ready {
		t.Fatal("required deterministic MCP server is not ready")
	}

	maxTokens := 16
	response, err := gw.Route(t.Context(), core.Request{
		Model: goal21Model,
		Messages: []core.Message{{
			Role: core.RoleUser, Content: "Call get_answer with q set to x, then answer with only its result.",
		}},
		MaxTokens: &maxTokens,
	})
	if err != nil {
		t.Fatalf("live MCP route failed: %s", liveErrorClass(err))
	}
	if len(response.Choices) == 0 || len(response.Choices[0].Message.ToolCalls) != 0 {
		t.Fatal("live MCP loop did not terminate with a final response")
	}
	message := response.Choices[0].Message
	if message.Content == "" && message.ReasoningContent == "" {
		t.Fatal("live MCP loop returned no normalized final output")
	}
	if live.calls.Load() != 2 || toolCalls.Load() != 1 {
		t.Fatalf("MCP loop calls provider=%d tool=%d, want 2 and 1", live.calls.Load(), toolCalls.Load())
	}
	requests := live.requestSnapshot()
	if len(requests) != 2 {
		t.Fatal("MCP wrapper did not capture both provider turns")
	}
	firstHasTool := false
	for _, tool := range requests[0].Tools {
		if tool.Function.Name == "get_answer" {
			firstHasTool = true
			break
		}
	}
	secondHasResult := false
	for _, message := range requests[1].Messages {
		if message.Role == core.RoleTool && message.ToolCallID != "" {
			secondHasResult = true
			break
		}
	}
	if !firstHasTool || !secondHasResult || response.Usage.TotalTokens <= 0 {
		t.Fatal("MCP injection, continuation, or aggregate usage invariant failed")
	}
	t.Logf("scenario=S08 provider_turns=2 tool_calls=1 aggregate_tokens=%d raw_content_persisted=false",
		response.Usage.TotalTokens)
}

//nolint:gocyclo // one end-to-end restart scenario: five stores plus the gateway, each asserted in sequence
func TestLiveGoal21_PersistentRestartContainsNoCredentialMaterial(t *testing.T) {
	t.Setenv(models.CatalogFetchTimeoutEnv, "0")
	root := t.TempDir()
	paths := map[string]string{
		"keys":     filepath.Join(root, "keys.db"),
		"sessions": filepath.Join(root, "sessions.db"),
		"config":   filepath.Join(root, "config.db"),
		"requests": filepath.Join(root, "requests.db"),
		"audit":    filepath.Join(root, "audit.db"),
	}
	providerCredential := os.Getenv("OPENAI_API_KEY")
	if providerCredential == "" {
		t.Fatal("Goal 21 provider credential is unavailable")
	}
	cfg := config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: goal21ProviderID, Models: []string{goal21Model}}},
	}

	keyStore, err := repository.NewSQLiteStore(t.Context(), paths["keys"])
	if err != nil {
		t.Fatalf("open persistent key store: %T", err)
	}
	sessionStore, err := repository.NewSQLiteSessionStore(t.Context(), paths["sessions"])
	if err != nil {
		t.Fatalf("open persistent session store: %T", err)
	}
	configStore, err := repository.NewSQLiteConfigStore(t.Context(), paths["config"])
	if err != nil {
		t.Fatalf("open persistent config store: %T", err)
	}
	logStore, err := requestlog.NewSQLiteWriter(t.Context(), paths["requests"])
	if err != nil {
		t.Fatalf("open persistent request-log store: %T", err)
	}
	auditStore, err := repository.NewSQLiteAuditStore(t.Context(), paths["audit"])
	if err != nil {
		t.Fatalf("open persistent audit store: %T", err)
	}

	key, err := keyStore.Create(t.Context(), "goal21-persistent", []string{adminmodel.ScopeAdmin}, nil)
	if err != nil {
		t.Fatalf("create persistent key: %T", err)
	}
	_, sessionToken, err := sessionStore.CreateSession(
		t.Context(), "goal21", key.ID, key.Scopes, repository.DefaultSessionTTL,
	)
	if err != nil {
		t.Fatalf("create persistent session: %T", err)
	}
	if err := configStore.Save(t.Context(), cfg); err != nil {
		t.Fatalf("persist gateway config: %T", err)
	}
	cost := 0.000001
	if err := logStore.Write(t.Context(), requestlog.Entry{
		TraceID: "goal21-persistent-trace", Stage: "after_request", Model: goal21Model,
		Provider: goal21ProviderID, APIKeyID: key.ID, TotalTokens: 1, CostUSD: &cost,
	}); err != nil {
		t.Fatalf("persist request log: %T", err)
	}
	if err := auditStore.Append(t.Context(), adminmodel.AuditEntry{
		Action: "key.create", Actor: "goal21", ActorID: key.ID, TargetID: key.ID,
		Outcome: adminmodel.AuditOK, TraceID: "goal21-persistent-trace",
	}); err != nil {
		t.Fatalf("persist audit row: %T", err)
	}
	for name, closeFn := range map[string]func() error{
		"keys": keyStore.Close, "sessions": sessionStore.Close, "config": configStore.Close,
		"requests": logStore.Close, "audit": auditStore.Close,
	} {
		if err := closeFn(); err != nil {
			t.Fatalf("close %s store: %v", name, err)
		}
	}

	files, err := filepath.Glob(filepath.Join(root, "*"))
	if err != nil {
		t.Fatalf("enumerate persistent stores: %T", err)
	}
	for _, path := range files {
		data, err := os.ReadFile(path) //nolint:gosec // path comes from this test's own t.TempDir() enumeration
		if err != nil {
			t.Fatalf("read persistent store for secret scan: %T", err)
		}
		if bytes.Contains(data, []byte(providerCredential)) ||
			bytes.Contains(data, []byte(key.Key)) ||
			bytes.Contains(data, []byte(sessionToken)) {
			t.Fatal("plaintext credential material reached a persistent store")
		}
	}

	restartedKeys, err := repository.NewSQLiteStore(t.Context(), paths["keys"])
	if err != nil {
		t.Fatalf("reopen key store: %T", err)
	}
	defer func() { _ = restartedKeys.Close() }()
	restartedSessions, err := repository.NewSQLiteSessionStore(t.Context(), paths["sessions"])
	if err != nil {
		t.Fatalf("reopen session store: %T", err)
	}
	defer func() { _ = restartedSessions.Close() }()
	restartedConfig, err := repository.NewSQLiteConfigStore(t.Context(), paths["config"])
	if err != nil {
		t.Fatalf("reopen config store: %T", err)
	}
	defer func() { _ = restartedConfig.Close() }()
	restartedLogs, err := requestlog.NewSQLiteWriter(t.Context(), paths["requests"])
	if err != nil {
		t.Fatalf("reopen request-log store: %T", err)
	}
	defer func() { _ = restartedLogs.Close() }()
	restartedAudit, err := repository.NewSQLiteAuditStore(t.Context(), paths["audit"])
	if err != nil {
		t.Fatalf("reopen audit store: %T", err)
	}
	defer func() { _ = restartedAudit.Close() }()

	if _, ok := restartedKeys.Get(t.Context(), key.ID); !ok {
		t.Fatal("API key metadata did not survive restart")
	}
	if _, ok := restartedSessions.ValidateSession(t.Context(), sessionToken); !ok {
		t.Fatal("hashed dashboard session did not survive restart")
	}
	if rows, err := restartedLogs.List(t.Context(), requestlog.Query{}); err != nil || rows.Total != 1 {
		t.Fatalf("request log did not survive restart: total=%d err=%v", rows.Total, err)
	}
	if rows, err := restartedAudit.List(t.Context(), adminmodel.AuditQuery{}); err != nil || rows.Total != 1 {
		t.Fatalf("audit row did not survive restart: total=%d err=%v", rows.Total, err)
	}

	restartedGateway, err := aigateway.New(config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "cohere"}},
	})
	if err != nil {
		t.Fatalf("construct restarted gateway: %v", err)
	}
	defer func() { _ = restartedGateway.Close() }()
	manager, err := repository.NewGatewayConfigManager(restartedGateway, restartedConfig)
	if err != nil {
		t.Fatalf("adopt persisted config: %T", err)
	}
	if !manager.AdoptedStoredConfig() || manager.GetConfig().Targets[0].VirtualKey != goal21ProviderID {
		t.Fatal("restart did not adopt the persisted live target")
	}
	restartedGateway.RegisterProvider(goal21LiveProvider(t))
	if !restartedGateway.Readiness().Ready {
		t.Fatal("restarted gateway is not ready with the live provider registered")
	}
	t.Log("scenario=S11 stores=5 restarted=true provider_credential_persisted=false key_and_session_plaintext_persisted=false")
}
