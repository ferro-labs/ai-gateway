package plugin

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime/debug"
	"sync"

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
	before      []Plugin
	after       []Plugin
	onErr       []Plugin
	mu          sync.RWMutex
	lifecycleMu sync.Mutex
	lifecycle   *sync.Cond
	active      int
	closed      bool
}

// NewManager creates a new plugin manager.
func NewManager() *Manager {
	m := &Manager{}
	m.lifecycle = sync.NewCond(&m.lifecycleMu)
	return m
}

// Acquire marks the manager as in use until the returned release function is
// called. Close waits for active users before releasing plugin resources.
func (m *Manager) Acquire() func() {
	m.lifecycleMu.Lock()
	m.ensureLifecycleLocked()
	if m.closed {
		m.lifecycleMu.Unlock()
		return func() {}
	}
	m.active++
	m.lifecycleMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			m.lifecycleMu.Lock()
			m.active--
			if m.active == 0 {
				m.lifecycle.Broadcast()
			}
			m.lifecycleMu.Unlock()
		})
	}
}

// Register registers a plugin at the given stage.
func (m *Manager) Register(stage Stage, p Plugin) error {
	m.mu.Lock()
	defer m.mu.Unlock()

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
	m.mu.RLock()
	plugins := m.before
	m.mu.RUnlock()
	for _, p := range plugins {
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
	m.mu.RLock()
	plugins := m.after
	m.mu.RUnlock()
	for _, p := range plugins {
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
	m.mu.RLock()
	plugins := m.onErr
	m.mu.RUnlock()
	for _, p := range plugins {
		if err := m.executePlugin(ctx, p, pctx, string(StageOnError)); err != nil {
			logging.Logger.Warn("on-error plugin error", "plugin", p.Name(), "error", err)
		}
	}
}

// executePlugin runs a single plugin under a child OTel span. When no
// global TracerProvider is installed the span is a no-op and adds
// effectively zero overhead. The span records the rejection outcome
// via ferro.plugin.outcome / ferro.plugin.reason attributes.
func (m *Manager) executePlugin(ctx context.Context, p Plugin, pctx *Context, stage string) (err error) {
	spanName := "plugin." + stage + "." + p.Name()
	ctx, span := pluginTracer().Start(ctx, spanName, trace.WithAttributes(
		pluginAttrs(p.Name(), string(p.Type()), stage)...,
	))
	defer func() {
		if recovered := recover(); recovered != nil {
			stack := debug.Stack()
			err = fmt.Errorf("plugin %s panicked at %s", p.Name(), stage)
			logging.Logger.Error("plugin panicked",
				"plugin", p.Name(),
				"stage", stage,
				"panic", recovered,
				"stack", string(stack),
			)
			span.SetAttributes(attribute.String(observability.AttrFerroPluginOutcome, "error"))
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}()

	err = p.Execute(ctx, pctx)

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

// Close starts closing the manager, releases each registered plugin instance
// once, and clears the manager. If requests are still using this manager, Close
// returns immediately and cleanup runs after the active users drain.
func (m *Manager) Close() error {
	m.lifecycleMu.Lock()
	m.ensureLifecycleLocked()
	if m.closed {
		m.lifecycleMu.Unlock()
		return nil
	}
	m.closed = true
	if m.active > 0 {
		m.lifecycleMu.Unlock()
		go m.closeWhenDrained()
		return nil
	}
	m.lifecycleMu.Unlock()

	return m.closePlugins()
}

func (m *Manager) closeWhenDrained() {
	m.lifecycleMu.Lock()
	m.ensureLifecycleLocked()
	for m.active > 0 {
		m.lifecycle.Wait()
	}
	m.lifecycleMu.Unlock()

	if err := m.closePlugins(); err != nil {
		logging.Logger.Warn("deferred plugin close failed", "error", err)
	}
}

func (m *Manager) closePlugins() error {
	m.mu.Lock()
	plugins := make([]Plugin, 0, len(m.before)+len(m.after)+len(m.onErr))
	plugins = append(plugins, m.before...)
	plugins = append(plugins, m.after...)
	plugins = append(plugins, m.onErr...)
	m.before = nil
	m.after = nil
	m.onErr = nil
	m.mu.Unlock()

	var err error
	for _, p := range uniquePluginInstances(plugins) {
		if closeErr := p.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("plugin %s close failed: %w", p.Name(), closeErr))
		}
	}
	return err
}

func (m *Manager) ensureLifecycleLocked() {
	if m.lifecycle == nil {
		m.lifecycle = sync.NewCond(&m.lifecycleMu)
	}
}

func uniquePluginInstances(plugins []Plugin) []Plugin {
	unique := make([]Plugin, 0, len(plugins))
	seen := make(map[pluginInstanceKey]struct{}, len(plugins))
	for _, p := range plugins {
		v := reflect.ValueOf(p)
		if v.Kind() != reflect.Pointer || v.IsNil() {
			unique = append(unique, p)
			continue
		}
		key := pluginInstanceKey{typ: v.Type(), ptr: v.Pointer()}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, p)
	}
	return unique
}

type pluginInstanceKey struct {
	typ reflect.Type
	ptr uintptr
}

// HasPlugins returns true if any plugins are registered.
func (m *Manager) HasPlugins() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.before)+len(m.after)+len(m.onErr) > 0
}
