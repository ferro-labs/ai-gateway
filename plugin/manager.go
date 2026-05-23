package plugin

import (
	"context"
	"fmt"

	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/ferro-labs/ai-gateway/observability"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const defaultRejectionReason = "rejected"

// pluginTracer returns the OTel tracer used for plugin-stage child
// spans. When no global TracerProvider is installed (the default no-op
// case) this is a cheap no-op tracer — calls are inlined and emit
// nothing.
func pluginTracer() trace.Tracer {
	return otel.Tracer("github.com/ferro-labs/ai-gateway/plugin")
}

// pluginAttrs returns the standard span attribute set for a plugin
// invocation. Keys are sourced from the observability package constants
// so attribute names stay in sync with the schema doc.
func pluginAttrs(name, kind, stage string) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String(observability.AttrFerroPluginName, name),
		attribute.String(observability.AttrFerroPluginKind, kind),
		attribute.String(observability.AttrFerroPluginStage, stage),
	}
}

// Manager manages plugin lifecycle and execution.
type Manager struct {
	before []Plugin
	after  []Plugin
	onErr  []Plugin
}

// NewManager creates a new plugin manager.
func NewManager() *Manager {
	return &Manager{}
}

// Register registers a plugin at the given stage.
func (m *Manager) Register(stage Stage, p Plugin) error {
	switch stage {
	case StageBeforeRequest:
		m.before = append(m.before, p)
	case StageAfterRequest:
		m.after = append(m.after, p)
	case StageOnError:
		m.onErr = append(m.onErr, p)
	default:
		return fmt.Errorf("unknown plugin stage: %s", stage)
	}
	logging.Logger.Info("plugin registered", "name", p.Name(), "type", p.Type(), "stage", stage)
	return nil
}

// RunBefore executes all before-request plugins. Returns an error if a plugin
// rejects the request.
func (m *Manager) RunBefore(ctx context.Context, pctx *Context) error {
	for _, p := range m.before {
		err := m.executePlugin(ctx, p, pctx, string(StageBeforeRequest))
		if err != nil {
			if pctx.Reject {
				reason := pctx.Reason
				if reason == "" {
					reason = err.Error()
				}
				return &RejectionError{Plugin: p.Name(), PluginType: p.Type(), Stage: StageBeforeRequest, Reason: reason}
			}
			return fmt.Errorf("plugin %s failed: %w", p.Name(), err)
		}
		if pctx.Reject {
			reason := pctx.Reason
			if reason == "" {
				reason = defaultRejectionReason
			}
			return &RejectionError{Plugin: p.Name(), PluginType: p.Type(), Stage: StageBeforeRequest, Reason: reason}
		}
		if pctx.Skip {
			break
		}
	}
	return nil
}

// RunAfter executes all after-request plugins.
func (m *Manager) RunAfter(ctx context.Context, pctx *Context) error {
	for _, p := range m.after {
		err := m.executePlugin(ctx, p, pctx, string(StageAfterRequest))
		if pctx.Reject {
			reason := pctx.Reason
			if reason == "" {
				if err != nil {
					reason = err.Error()
				} else {
					reason = defaultRejectionReason
				}
			}
			return &RejectionError{Plugin: p.Name(), PluginType: p.Type(), Stage: StageAfterRequest, Reason: reason}
		}
		if err != nil {
			logging.Logger.Warn("after-request plugin error", "plugin", p.Name(), "error", err)
		}
		if pctx.Skip {
			break
		}
	}
	return nil
}

// RunOnError executes all on-error plugins.
func (m *Manager) RunOnError(ctx context.Context, pctx *Context) {
	for _, p := range m.onErr {
		if err := m.executePlugin(ctx, p, pctx, string(StageOnError)); err != nil {
			logging.Logger.Warn("on-error plugin error", "plugin", p.Name(), "error", err)
		}
	}
}

// executePlugin runs a single plugin under a child OTel span. When no
// global TracerProvider is installed the span is a no-op and adds
// effectively zero overhead. The span records the rejection outcome
// via ferro.plugin.outcome / ferro.plugin.reason attributes.
func (m *Manager) executePlugin(ctx context.Context, p Plugin, pctx *Context, stage string) error {
	spanName := "plugin." + stage + "." + p.Name()
	ctx, span := pluginTracer().Start(ctx, spanName, trace.WithAttributes(
		pluginAttrs(p.Name(), string(p.Type()), stage)...,
	))
	defer span.End()

	err := p.Execute(ctx, pctx)

	switch {
	case pctx.Reject:
		span.SetAttributes(attribute.String(observability.AttrFerroPluginOutcome, "rejected"))
		if pctx.Reason != "" {
			span.SetAttributes(attribute.String(observability.AttrFerroPluginReason, pctx.Reason))
		}
	case err != nil:
		span.SetAttributes(attribute.String(observability.AttrFerroPluginOutcome, "error"))
		span.SetStatus(codes.Error, err.Error())
	default:
		span.SetAttributes(attribute.String(observability.AttrFerroPluginOutcome, "ok"))
	}
	return err
}

// HasPlugins returns true if any plugins are registered.
func (m *Manager) HasPlugins() bool {
	return len(m.before)+len(m.after)+len(m.onErr) > 0
}
