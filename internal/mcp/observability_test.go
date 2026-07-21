package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/metrics"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// counterValue reads one labelled sample out of a CounterVec. Returns 0 when
// the series does not exist yet, which is what "never incremented" looks like.
func counterValue(t *testing.T, vec *prometheus.CounterVec, labels ...string) float64 {
	t.Helper()
	c, err := vec.GetMetricWithLabelValues(labels...)
	if err != nil {
		t.Fatalf("GetMetricWithLabelValues(%v): %v", labels, err)
	}
	var m dto.Metric
	if err := c.(prometheus.Metric).Write(&m); err != nil {
		t.Fatalf("write metric: %v", err)
	}
	return m.GetCounter().GetValue()
}

func gaugeValue(t *testing.T, vec *prometheus.GaugeVec, labels ...string) float64 {
	t.Helper()
	g, err := vec.GetMetricWithLabelValues(labels...)
	if err != nil {
		t.Fatalf("GetMetricWithLabelValues(%v): %v", labels, err)
	}
	var m dto.Metric
	if err := g.Write(&m); err != nil {
		t.Fatalf("write metric: %v", err)
	}
	return m.GetGauge().GetValue()
}

// isErrorClient returns a successful RPC carrying a tool-level failure — the
// shape a server uses to say "the tool ran and it went wrong".
type isErrorClient struct{}

func (isErrorClient) Initialize(context.Context) (*ServerInfo, error) { return &ServerInfo{}, nil }
func (isErrorClient) ListTools(context.Context) ([]Tool, error)       { return nil, nil }
func (isErrorClient) CallTool(context.Context, string, json.RawMessage) (*ToolCallResult, error) {
	return &ToolCallResult{
		Content: []ContentBlock{{Type: "text", Text: "disk on fire"}},
		IsError: true,
	}, nil
}
func (isErrorClient) Close() error { return nil }

// TestIsErrorIsMeteredAsFailure is the regression test for tool failures being
// counted as successes.
//
// ToolCallResult.IsError had no production reader: a server answering
// {"isError": true} on every call incremented status="ok", audited "ok", and
// set an Ok span status, so a wholly broken tool was indistinguishable from a
// working one on the dashboard.
func TestIsErrorIsMeteredAsFailure(t *testing.T) {
	reg := registryWith(map[string]mcpClient{"srv-iserr": isErrorClient{}})
	reg.mu.Lock()
	reg.servers["srv-iserr"].tools = []Tool{{Name: "tool_iserr"}}
	reg.toolMap["tool_iserr"] = "srv-iserr"
	reg.mu.Unlock()

	var (
		mu       sync.Mutex
		statuses []string
		errMsgs  []string
	)
	audited := make(chan struct{}, 1)
	audit := func(_ context.Context, _, _, status string, _ int, errMsg string) {
		mu.Lock()
		statuses = append(statuses, status)
		errMsgs = append(errMsgs, errMsg)
		mu.Unlock()
		audited <- struct{}{}
	}

	okBefore := counterValue(t, metricToolCallsTotal, "srv-iserr", "tool_iserr", "ok")
	errBefore := counterValue(t, metricToolCallsTotal, "srv-iserr", "tool_iserr", "error")

	exec := NewExecutor(reg, 5, audit)
	msg := exec.executeToolCall(context.Background(), toolCallNamed("tool_iserr"))

	// The tool's own message still reaches the LLM — this changes how the call
	// is *reported*, not what the model is told.
	if msg.Content != "disk on fire" {
		t.Errorf("content = %q, want the tool's error text passed through", msg.Content)
	}

	if got := counterValue(t, metricToolCallsTotal, "srv-iserr", "tool_iserr", "error"); got != errBefore+1 {
		t.Errorf("status=error counter = %v, want %v: an isError result was not metered as a failure", got, errBefore+1)
	}
	if got := counterValue(t, metricToolCallsTotal, "srv-iserr", "tool_iserr", "ok"); got != okBefore {
		t.Errorf("status=ok counter = %v, want %v: an isError result was metered as a success", got, okBefore)
	}

	<-audited
	mu.Lock()
	defer mu.Unlock()
	if len(statuses) != 1 || statuses[0] != "error" {
		t.Errorf("audit status = %v, want [error]", statuses)
	}
	if len(errMsgs) != 1 || errMsgs[0] != "disk on fire" {
		t.Errorf("audit error message = %v, want the tool's error text", errMsgs)
	}
}

// TestIsErrorFalseStillMetersOk pins the other half: an ordinary successful
// call must keep reporting ok, so the fix above cannot have inverted the sense.
func TestIsErrorFalseStillMetersOk(t *testing.T) {
	reg := buildReadyRegistry(t, []string{"tool_ok"})
	defer func() { _ = reg.Close() }()

	okBefore := counterValue(t, metricToolCallsTotal, "exec-srv", "tool_ok", "ok")
	errBefore := counterValue(t, metricToolCallsTotal, "exec-srv", "tool_ok", "error")

	exec := NewExecutor(reg, 5, nil)
	exec.executeToolCall(context.Background(), toolCallNamed("tool_ok"))

	if got := counterValue(t, metricToolCallsTotal, "exec-srv", "tool_ok", "ok"); got != okBefore+1 {
		t.Errorf("status=ok counter = %v, want %v", got, okBefore+1)
	}
	if got := counterValue(t, metricToolCallsTotal, "exec-srv", "tool_ok", "error"); got != errBefore {
		t.Errorf("status=error counter = %v, want %v", got, errBefore)
	}
}

// TestUnknownToolLabelIsBounded is the regression test for unbounded label
// cardinality on the unknown-tool counter.
//
// executeToolCall labelled the counter with tc.Function.Name verbatim. That
// string is model output, so a model emitting invented tool names minted a
// permanent Prometheus time series per name — the same defect class as the
// model label.
func TestUnknownToolLabelIsBounded(t *testing.T) {
	reg := NewRegistry()
	exec := NewExecutor(reg, 5, nil)

	before := counterValue(t, metricUnknownToolCallsTotal, metrics.UnknownToolLabel)

	// Three names no server ever advertised, as a model might hallucinate them.
	invented := []string{"hallucinated_tool_a", "hallucinated_tool_b", "hallucinated_tool_c"}
	for _, name := range invented {
		exec.executeToolCall(context.Background(), toolCallNamed(name))
	}

	if got := counterValue(t, metricUnknownToolCallsTotal, metrics.UnknownToolLabel); got != before+3 {
		t.Errorf("bounded label counter = %v, want %v: unknown names were not collapsed", got, before+3)
	}
	for _, name := range invented {
		if got := counterValue(t, metricUnknownToolCallsTotal, name); got != 0 {
			t.Errorf("a series was minted for model-supplied name %q (value %v)", name, got)
		}
	}
}

// TestKnownToolKeepsItsLabelWhenServerDies pins the complement: a tool the
// registry actually indexed keeps its real name, because a real tool that
// stopped resolving is the case worth alerting on. Collapsing everything to the
// sentinel would have thrown that away.
func TestKnownToolKeepsItsLabelWhenServerDies(t *testing.T) {
	c := &closedTransportClient{}
	reg := registryWith(map[string]mcpClient{"srv-known": c})
	reg.mu.Lock()
	reg.servers["srv-known"].tools = []Tool{{Name: "real_tool"}}
	reg.toolMap["real_tool"] = "srv-known"
	reg.mu.Unlock()

	exec := NewExecutor(reg, 5, nil)
	// First call fails with transport closed and withdraws the server.
	exec.executeToolCall(context.Background(), toolCallNamed("real_tool"))

	before := counterValue(t, metricUnknownToolCallsTotal, "real_tool")
	// Second call no longer resolves, so it lands on the unknown path — but the
	// name is still in toolMap, so it is bounded and keeps its identity.
	exec.executeToolCall(context.Background(), toolCallNamed("real_tool"))

	if got := counterValue(t, metricUnknownToolCallsTotal, "real_tool"); got != before+1 {
		t.Errorf("known tool counter = %v, want %v: a registry-indexed name lost its label", got, before+1)
	}
}

// TestInitFailureIsCounted is the regression test for a dead MCP fleet being
// indistinguishable from an idle one: initServer's failure paths incremented
// nothing, so nothing could be alerted on.
func TestInitFailureIsCounted(t *testing.T) {
	const name = "init-fails"
	before := counterValue(t, metrics.MCPServerInitFailures, name)

	reg := NewRegistry()
	defer func() { _ = reg.Close() }()
	// A URL that refuses connections makes the handshake fail deterministically.
	reg.RegisterConfig(ServerConfig{Name: name, URL: "http://127.0.0.1:1/mcp", TimeoutSeconds: 1})

	var gotErr error
	reg.InitializeAll(context.Background(), func(_ string, err error) { gotErr = err })
	if gotErr == nil {
		t.Fatal("precondition: expected the handshake to fail")
	}

	if got := counterValue(t, metrics.MCPServerInitFailures, name); got != before+1 {
		t.Errorf("init failure counter = %v, want %v", got, before+1)
	}
	if got := gaugeValue(t, metrics.MCPServerUp, name); got != 0 {
		t.Errorf("server_up gauge = %v, want 0 for a server that never initialized", got)
	}

	// The reason is retained rather than discarded.
	st := reg.Status()
	if len(st) != 1 || st[0].LastError == "" {
		t.Errorf("Status() = %+v, want one entry carrying the failure reason", st)
	}
}

// TestServerUpGaugeTracksReadiness proves the gauge follows the same ready bit
// everything else does, in both directions.
func TestServerUpGaugeTracksReadiness(t *testing.T) {
	reg := buildReadyRegistry(t, []string{"gauge_tool"})
	defer func() { _ = reg.Close() }()

	if got := gaugeValue(t, metrics.MCPServerUp, "exec-srv"); got != 1 {
		t.Fatalf("server_up = %v after successful init, want 1", got)
	}

	reg.mu.Lock()
	client := reg.servers["exec-srv"].client
	reg.mu.Unlock()
	reg.markUnready("exec-srv", client, errors.New("gone"))

	if got := gaugeValue(t, metrics.MCPServerUp, "exec-srv"); got != 0 {
		t.Errorf("server_up = %v after the server was withdrawn, want 0", got)
	}
}

// TestStatusReportsRequiredFlag pins that Status carries the config's required
// bit through, since /readyz gates on exactly that.
func TestStatusReportsRequiredFlag(t *testing.T) {
	reg := NewRegistry()
	defer func() { _ = reg.Close() }()
	reg.RegisterConfig(ServerConfig{Name: "opt", URL: "http://127.0.0.1:1/mcp"})
	reg.RegisterConfig(ServerConfig{Name: "req", URL: "http://127.0.0.1:1/mcp", Required: true})

	st := reg.Status()
	if len(st) != 2 {
		t.Fatalf("Status() returned %d entries, want 2", len(st))
	}
	if st[0].Name != "opt" || st[0].Required {
		t.Errorf("entry 0 = %+v, want opt with Required=false", st[0])
	}
	if st[1].Name != "req" || !st[1].Required {
		t.Errorf("entry 1 = %+v, want req with Required=true", st[1])
	}
}
