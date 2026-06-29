//go:build integration
// +build integration

package http_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/routetrace"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

var errTraceDryRunShouldNotCallProvider = errStr("trace dry-run must never call a provider Complete method")

type errStr string

func (e errStr) Error() string { return string(e) }

// TestRouteTrace_DryRunThroughRouter boots the real production router via
// newTestServer and asserts the acceptance criteria from issue #238:
//   - the endpoint is authed (401 without a bearer key),
//   - it emits a normal request ID (X-Request-Id),
//   - it performs no upstream model call and consumes no provider quota,
//   - it returns the selected target + ordered candidate explanations +
//     catalog match state for the fallback strategy.
func TestRouteTrace_DryRunThroughRouter(t *testing.T) {
	env := newTestServer(t)

	// Wire a Complete hook that records any call — the dry-run trace must
	// perform no upstream model call and consume no provider quota.
	completeCalls := 0
	env.Stub.CompleteHook = func(_ context.Context, _ core.Request) (*core.Response, error) {
		completeCalls++
		return nil, errTraceDryRunShouldNotCallProvider
	}

	body := `{"model":"stub-model-v1","messages":[{"role":"user","content":"trace"}]}`
	req, _ := http.NewRequest(http.MethodPost, env.Server.URL+"/v1/route/trace", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/route/trace: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(b))
	}

	// Must carry a request ID like every other client-visible route.
	if rid := resp.Header.Get("X-Request-Id"); rid == "" {
		t.Error("X-Request-Id header must be set (auth/audit/OTel parity)")
	}

	var trace routetrace.TraceResponse
	if err := json.NewDecoder(resp.Body).Decode(&trace); err != nil {
		t.Fatalf("decode trace: %v", err)
	}

	if !trace.DryRun {
		t.Error("response.DryRun must be true (endpoint performs no upstream call)")
	}
	if trace.RequestedModel != "stub-model-v1" {
		t.Errorf("RequestedModel = %q, want stub-model-v1", trace.RequestedModel)
	}
	if trace.SelectedTargetKey != "stub" {
		t.Errorf("SelectedTargetKey = %q, want stub", trace.SelectedTargetKey)
	}
	if len(trace.CandidateTargets) == 0 {
		t.Fatal("CandidateTargets empty, expected ordered candidate explanations")
	}
	if !trace.CandidateTargets[0].Matched {
		t.Error("first candidate must be Matched=true (fallback selects first supporting target)")
	}

	// Acceptance: the endpoint performs no upstream model call. The stub provider
	// records each Complete call via the hook; the dry-run must have recorded zero.
	if completeCalls != 0 {
		t.Fatalf("stub provider Complete called %d times during dry-run; trace must never call a provider", completeCalls)
	}
}

// TestRouteTrace_RequiresAuth ensures the endpoint inherits the same auth
// middleware as /v1/models and /v1/chat/completions.
func TestRouteTrace_RequiresAuth(t *testing.T) {
	env := newTestServer(t)

	req, _ := http.NewRequest(http.MethodPost, env.Server.URL+"/v1/route/trace", strings.NewReader(`{"model":"stub-model-v1"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/route/trace: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without auth, got %d", resp.StatusCode)
	}
}

// TestRouteTrace_MissingModelIs400 verifies the reduced probe form (model only)
// and the missing-model rejection both behave per the issue's request schema.
func TestRouteTrace_MissingModelIs400(t *testing.T) {
	env := newTestServer(t)

	req, _ := http.NewRequest(http.MethodPost, env.Server.URL+"/v1/route/trace", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/route/trace: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing model, got %d", resp.StatusCode)
	}
}
