package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	rtrace "runtime/trace"
	"time"
	"unicode/utf8"

	"github.com/ferro-labs/ai-gateway/internal/metrics"
	gwotel "github.com/ferro-labs/ai-gateway/internal/otel"
	"github.com/ferro-labs/ai-gateway/observability"
	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// mcpTracerName is the OpenTelemetry instrumentation scope name used for
// MCP tool-call child spans. Matches the package import path so backends
// can identify the source of these spans.
const mcpTracerName = "github.com/ferro-labs/ai-gateway/internal/mcp"

// maxToolCallsPerTurn bounds how many tool calls a single LLM response may
// trigger. maxCallDepth limits how many *turns* the agentic loop runs, but
// nothing limited the calls within one turn: a response carrying 20 000
// tool_calls produced 20 000 executions and 20 001 conversation messages,
// each re-sent to the provider on every subsequent turn.
const maxToolCallsPerTurn = 64

// mcpTracer returns the OpenTelemetry tracer for MCP-instrumentation
// spans. When OTel is not configured by the gateway, the global
// provider returns a no-op tracer and spans are zero-cost.
func mcpTracer() trace.Tracer {
	return otel.Tracer(mcpTracerName)
}

// AuditFn is an optional callback invoked after every MCP tool invocation.
// serverName and toolName identify the call; status is "ok" or "error";
// latencyMs is the wall-clock time of the CallTool RPC; errMsg is non-empty
// on failure. Implementations must be non-blocking.
type AuditFn func(ctx context.Context, serverName, toolName, status string, latencyMs int, errMsg string)

// Prometheus metrics — registered once at program start.
var (
	metricToolCallsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "ferrogw",
		Subsystem: "mcp",
		Name:      "tool_calls_total",
		Help:      "Total number of MCP tool calls made.",
	}, []string{"server_name", "tool_name", "status"})

	metricToolCallDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "ferrogw",
		Subsystem: "mcp",
		Name:      "tool_call_duration_seconds",
		Help:      "Latency of individual MCP tool calls in seconds.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"server_name", "tool_name"})

	metricUnknownToolCallsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "ferrogw",
		Subsystem: "mcp",
		Name:      "unknown_tool_calls_total",
		Help:      "Tool calls for tools not found in any registered MCP server.",
	}, []string{"tool_name"})
)

// Executor runs the agentic tool-call loop on top of a Registry.
// It is safe to use concurrently.
type Executor struct {
	registry     *Registry
	maxCallDepth int
	auditFn      AuditFn // optional; nil disables audit logging
}

// NewExecutor creates an Executor backed by the given Registry.
// maxCallDepth caps the number of tool-call iterations per request;
// a value <= 0 defaults to 5.
// auditFn, if non-nil, is called after every tool invocation with timing data.
func NewExecutor(registry *Registry, maxCallDepth int, auditFn AuditFn) *Executor {
	if maxCallDepth <= 0 {
		maxCallDepth = 5
	}
	return &Executor{registry: registry, maxCallDepth: maxCallDepth, auditFn: auditFn}
}

// callAuditFn dispatches the audit callback asynchronously in its own goroutine
// so that a slow or panicking user-supplied hook cannot block or crash the
// tool-call loop.  It is a no-op when auditFn is nil.
func (e *Executor) callAuditFn(ctx context.Context, serverName, toolName, status string, latencyMs int, errMsg string) {
	if e.auditFn == nil {
		return
	}
	fn := e.auditFn
	go func() {
		defer func() {
			// Swallow any panic from the user-supplied callback — audit logging
			// must never crash tool execution.
			recover() //nolint:errcheck // recover returns the panic value, intentionally discarded here
		}()
		fn(ctx, serverName, toolName, status, latencyMs, errMsg)
	}()
}

// ShouldContinueLoop reports whether the LLM response contains pending tool
// calls that should be resolved and the depth limit has not been reached.
func (e *Executor) ShouldContinueLoop(resp *core.Response, depth int) bool {
	if depth >= e.maxCallDepth {
		return false
	}
	if resp == nil || len(resp.Choices) == 0 {
		return false
	}
	for _, ch := range resp.Choices {
		// Only a fully MCP-owned turn arms the loop. A turn containing any
		// caller-supplied call must reach the client with its finish_reason
		// intact — the gateway cannot answer those, and answering only its own
		// would leave unmatched tool_call_ids that the provider rejects.
		if e.ownsAll(ch.Message.ToolCalls) {
			return true
		}
	}
	return false
}

// ResolvePendingToolCalls executes all tool calls present in the response,
// returning the new messages (one assistant message + one tool message per
// call) to append to the conversation before the next LLM turn.
func (e *Executor) ResolvePendingToolCalls(ctx context.Context, resp *core.Response) ([]core.Message, error) {
	ctx, task := rtrace.NewTask(ctx, "mcp.resolve_tool_calls")
	defer task.End()

	if resp == nil || len(resp.Choices) == 0 {
		return nil, nil
	}

	var extra []core.Message
	budget := maxToolCallsPerTurn

	for _, ch := range resp.Choices {
		calls := ch.Message.ToolCalls
		if len(calls) == 0 {
			continue
		}

		// The gateway can only answer calls it owns, and a provider rejects an
		// assistant turn whose tool_call_ids are not all answered. So a turn
		// mixing MCP-owned and caller-supplied calls cannot be continued at all:
		// executing half of it would turn a working request into a 400. Hand the
		// whole turn back instead and let the client resolve it.
		if !e.ownsAll(calls) {
			continue
		}

		if budget <= 0 {
			break
		}
		if len(calls) > budget {
			// Report the number actually executed, not the per-turn constant:
			// with several choices the remaining budget is what truncates, and
			// logging the constant would misstate it.
			slog.Warn("mcp: tool calls truncated for this turn",
				"turn_limit", maxToolCallsPerTurn,
				"executed", budget,
				"requested", len(calls),
			)
			calls = calls[:budget]
		}
		budget -= len(calls)

		// Preserve the assistant message (all fields, correct role) but carry
		// exactly the calls answered below. Truncating the executions without
		// truncating this list would leave unmatched tool_call_ids in the
		// continuation, which the provider rejects outright.
		assistantMsg := ch.Message
		if assistantMsg.Role == "" {
			assistantMsg.Role = core.RoleAssistant
		}
		assistantMsg.ToolCalls = calls
		extra = append(extra, assistantMsg)

		for _, tc := range calls {
			extra = append(extra, e.executeToolCall(ctx, tc))
		}
	}

	return extra, nil
}

// ownsAll reports whether every call in the turn belongs to a ready MCP server.
func (e *Executor) ownsAll(calls []core.ToolCall) bool {
	for _, tc := range calls {
		if !e.registry.Owns(tc.Function.Name) {
			return false
		}
	}
	return len(calls) > 0
}

// executeToolCall runs a single MCP tool call and returns the tool-role
// message to append to the conversation. It resolves the owning server,
// forwards the model-supplied arguments, records timing, metrics and an
// OTel child span, and invokes the audit hook. Unknown tools, call
// errors, and result-marshalling failures are folded into a JSON error
// payload on the returned message so the LLM can observe and report them.
func (e *Executor) executeToolCall(ctx context.Context, tc core.ToolCall) core.Message {
	toolName := tc.Function.Name
	serverName := e.registry.serverNameForTool(toolName)

	client, ok := e.registry.FindToolServer(toolName)
	if !ok {
		metricUnknownToolCallsTotal.WithLabelValues(boundedToolLabel(serverName, toolName)).Inc()
		// Return a friendly error result so the LLM can report it.
		notFoundPayload, _ := json.Marshal(map[string]string{
			"error": "tool " + toolName + " not found in any registered MCP server",
		})
		return core.Message{
			Role:       core.RoleTool,
			ToolCallID: tc.ID,
			Content:    string(notFoundPayload),
		}
	}

	// The LLM provides arguments as a JSON string; pass directly as RawMessage.
	args := json.RawMessage("{}")
	if tc.Function.Arguments != "" {
		args = json.RawMessage(tc.Function.Arguments)
	}

	// OTel child span around the MCP tool call. When the gateway has not
	// initialised an OTel provider this is a no-op span at zero cost.
	toolCtx, span := mcpTracer().Start(ctx, "mcp.call_tool",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String(observability.AttrFerroMCPServer, serverName),
			attribute.String(observability.AttrFerroMCPTool, toolName),
		),
	)
	// Deferred, not called inline after the RPC: a panic in a transport unwinds
	// past an inline End(), and the HTTP recover middleware keeps the process
	// alive, so the span would be orphaned for good rather than dying with the
	// process. Nothing below re-ends or reuses the span.
	defer span.End()

	// Guard against hung subprocesses (stdio) or slow servers (HTTP).
	callTimeout := e.registry.timeoutForServer(serverName)
	toolCtx, cancelCall := context.WithTimeout(toolCtx, callTimeout)
	// Same reasoning as the span: an inline cancel is skipped on panic, stranding
	// the timer until the timeout elapses. toolCtx is unused after the RPC, so
	// holding it to function end costs nothing.
	defer cancelCall()

	callStart := time.Now()
	var result *ToolCallResult
	var err error
	rtrace.WithRegion(toolCtx, "mcp.call_tool", func() {
		result, err = client.CallTool(toolCtx, toolName, args)
	})
	elapsed := time.Since(callStart)
	metricToolCallDuration.WithLabelValues(serverName, toolName).Observe(elapsed.Seconds())
	latencyMs := int(elapsed.Milliseconds())
	span.SetAttributes(attribute.Int64(observability.AttrFerroMCPLatencyMs, int64(latencyMs)))

	if err != nil {
		// A dead transport means the server process is gone, not that this one
		// call failed. Withdrawing it here is what stops the next thousand calls
		// from being routed to a dead client: the registry clears the ready bit,
		// so AllTools stops advertising its tools and FindToolServer stops
		// resolving them. Costs exactly one failed call to detect.
		//
		// A broken pipe counts as much as a closed transport: it is the shape a
		// death takes when a descendant still holds the pipes open, so the
		// transport never noticed and the write failed instead. See
		// isTransportDead for what deliberately does not qualify.
		if isTransportDead(err) {
			e.registry.markUnready(serverName, client, err)
		}
		metricToolCallsTotal.WithLabelValues(serverName, toolName, "error").Inc()
		// Relies on the non-blocking AuditFn contract: the per-call goroutine returns promptly.
		e.callAuditFn(ctx, serverName, toolName, "error", latencyMs, err.Error())
		gwotel.RecordSpanError(span, err)
		errPayload, _ := json.Marshal(map[string]string{"error": err.Error()})
		return core.Message{
			Role:       core.RoleTool,
			ToolCallID: tc.ID,
			Content:    string(errPayload),
		}
	}

	// Convert MCP content blocks to a plain string for the LLM.
	content, convErr := contentBlocksToString(result.Content)
	if convErr != nil {
		errPayload, _ := json.Marshal(map[string]string{"error": "could not marshal tool result: " + convErr.Error()})
		content = string(errPayload)
	}

	// A tool that answers with isError is a failed call. The RPC succeeded, so
	// err is nil and every signal here used to read "ok" — the metric, the audit
	// record, and the span status alike — which made a server returning nothing
	// but errors indistinguishable from one working perfectly. The result's own
	// content carries the reason and is what the LLM sees, so it is also the
	// most useful thing to attach to the failure.
	if result.IsError {
		metricToolCallsTotal.WithLabelValues(serverName, toolName, "error").Inc()
		e.callAuditFn(ctx, serverName, toolName, "error", latencyMs, truncateForSignal(content))
		gwotel.RecordSpanError(span, errors.New(truncateForSignal(content)))
	} else {
		metricToolCallsTotal.WithLabelValues(serverName, toolName, "ok").Inc()
		// Relies on the non-blocking AuditFn contract: the per-call goroutine returns promptly.
		e.callAuditFn(ctx, serverName, toolName, "ok", latencyMs, "")
		span.SetStatus(codes.Ok, "")
	}

	return core.Message{
		Role:       core.RoleTool,
		ToolCallID: tc.ID,
		Content:    content,
	}
}

// maxSignalLen bounds a tool-supplied error string copied into a span attribute
// or an audit record. Tool results are unbounded by design — the full text still
// reaches the LLM — but a multi-megabyte span attribute is a way to break an
// OTLP exporter, and no error message needs more than this to be diagnosed.
const maxSignalLen = 2048

// The cut is walked back to a rune boundary. A tool's output is arbitrary text,
// so slicing on a byte offset alone can land mid-rune and leave invalid UTF-8 in
// a span attribute and an audit record.
func truncateForSignal(s string) string {
	if len(s) <= maxSignalLen {
		return s
	}
	cut := maxSignalLen
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "… (truncated)"
}

// boundedToolLabel keeps a tool name as a Prometheus label only when the
// registry has actually indexed it, collapsing anything else to a constant.
//
// Tool names on this path come straight from model output, so a hallucinated
// name would otherwise mint a permanent time series — the same unbounded-label
// class as the model label. serverName is non-empty exactly when the name is in
// toolMap, which is the registry's own bounded set, so it doubles as the
// known-name test: a real tool whose server has just died keeps its name (the
// case worth alerting on), and an invented one does not.
func boundedToolLabel(serverName, toolName string) string {
	if serverName == "" {
		return metrics.UnknownToolLabel
	}
	return toolName
}

// contentBlocksToString serialises MCP content blocks into a string suitable
// for embedding in a chat message. Text blocks are concatenated; other block
// types are JSON-encoded.
func contentBlocksToString(blocks []ContentBlock) (string, error) {
	if len(blocks) == 0 {
		return "", nil
	}
	if len(blocks) == 1 && blocks[0].Type == "text" {
		return blocks[0].Text, nil
	}

	// Multiple blocks or non-text — return as JSON array.
	b, err := json.Marshal(blocks)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
