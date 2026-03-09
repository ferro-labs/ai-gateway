package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

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
}

// NewExecutor creates an Executor backed by the given Registry.
// maxCallDepth caps the number of tool-call iterations per request;
// a value <= 0 defaults to 5.
func NewExecutor(registry *Registry, maxCallDepth int) *Executor {
	if maxCallDepth <= 0 {
		maxCallDepth = 5
	}
	return &Executor{registry: registry, maxCallDepth: maxCallDepth}
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
		if len(ch.Message.ToolCalls) > 0 {
			return true
		}
	}
	return false
}

// ResolvePendingToolCalls executes all tool calls present in the response,
// returning the new messages (one assistant message + one tool message per
// call) to append to the conversation before the next LLM turn.
func (e *Executor) ResolvePendingToolCalls(ctx context.Context, resp *core.Response) ([]core.Message, error) {
	if resp == nil || len(resp.Choices) == 0 {
		return nil, nil
	}

	var extra []core.Message

	for _, ch := range resp.Choices {
		if len(ch.Message.ToolCalls) == 0 {
			continue
		}

		// Preserve the full assistant message from the LLM (all fields, correct role).
		assistantMsg := ch.Message
		if assistantMsg.Role == "" {
			assistantMsg.Role = core.RoleAssistant
		}
		extra = append(extra, assistantMsg)

		// Execute each tool call and collect results.
		for _, tc := range ch.Message.ToolCalls {
			toolName := tc.Function.Name
			serverName := e.registry.serverNameForTool(toolName)

			client, ok := e.registry.FindToolServer(toolName)
			if !ok {
				metricUnknownToolCallsTotal.WithLabelValues(toolName).Inc()
				// Return a friendly error result so the LLM can report it.
				notFoundPayload, _ := json.Marshal(map[string]string{
					"error": "tool " + toolName + " not found in any registered MCP server",
				})
				extra = append(extra, core.Message{
					Role:       core.RoleTool,
					ToolCallID: tc.ID,
					Content:    string(notFoundPayload),
				})
				continue
			}

			// The LLM provides arguments as a JSON string; pass directly as RawMessage.
			args := json.RawMessage("{}")
			if tc.Function.Arguments != "" {
				args = json.RawMessage(tc.Function.Arguments)
			}

			timer := prometheus.NewTimer(metricToolCallDuration.WithLabelValues(serverName, toolName))
			result, err := client.CallTool(ctx, toolName, args)
			timer.ObserveDuration()

			if err != nil {
				metricToolCallsTotal.WithLabelValues(serverName, toolName, "error").Inc()
				extra = append(extra, core.Message{
					Role:       core.RoleTool,
					ToolCallID: tc.ID,
					Content:    fmt.Sprintf(`{"error":%q}`, err.Error()),
				})
				continue
			}

			metricToolCallsTotal.WithLabelValues(serverName, toolName, "ok").Inc()

			// Convert MCP content blocks to a plain string for the LLM.
			content, err := contentBlocksToString(result.Content)
			if err != nil {
				errPayload, _ := json.Marshal(map[string]string{"error": "could not marshal tool result: " + err.Error()})
				content = string(errPayload)
			}

			extra = append(extra, core.Message{
				Role:       core.RoleTool,
				ToolCallID: tc.ID,
				Content:    content,
			})
		}
	}

	return extra, nil
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
