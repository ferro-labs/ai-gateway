package plugin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"runtime/debug"
	"sync"

	"github.com/ferro-labs/ai-gateway/observability"
)

const defaultRejectionReason = "rejected"

// pluginFailsClosed reports whether a plugin of the given type aborts the
// request when it errors or rejects. Observability-only plugins (logging,
// metrics, transform) fail open so a backend hiccup never takes down the
// request path; everything else — guardrails in particular — fails closed.
func pluginFailsClosed(pluginType PluginType) bool {
	switch pluginType {
	case TypeLogging, TypeMetrics, TypeTransform:
		return false
	default:
		return true
	}
}

// handlePluginFailure applies the per-type failure policy after a plugin
// stage runs. Fail-closed plugins turn an error or rejection into a
// RejectionError that aborts the stage; fail-open plugins have the same
// outcome logged and swallowed so the request continues.
func handlePluginFailure(p Plugin, stage Stage, pctx *Context, err error) error {
	if err == nil && !pctx.Reject {
		return nil
	}
	if pluginFailsClosed(p.Type()) {
		return rejectionErrorFor(p, stage, pctx, err)
	}

	rejected := pctx.Reject
	reason := rejectionReason(pctx, err)
	pctx.Reject = false
	pctx.Reason = ""
	if err != nil {
		slog.Default().Warn("fail-open plugin error ignored", "plugin", p.Name(), "type", p.Type(), "stage", stage, "error", err)
	}
	if rejected {
		slog.Default().Warn("fail-open plugin rejection ignored", "plugin", p.Name(), "type", p.Type(), "stage", stage, "reason", reason)
	}
	return nil
}

func rejectionErrorFor(p Plugin, stage Stage, pctx *Context, err error) *RejectionError {
	return &RejectionError{Plugin: p.Name(), PluginType: p.Type(), Stage: stage, Reason: rejectionReason(pctx, err)}
}

func rejectionReason(pctx *Context, err error) string {
	if pctx.Reason != "" {
		return pctx.Reason
	}
	if err != nil {
		return err.Error()
	}
	return defaultRejectionReason
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
	slog.Default().Info("plugin registered", "name", p.Name(), "type", p.Type(), "stage", stage)
	return nil
}

// RunBefore executes all before-request plugins. Fail-closed plugin errors or
// rejections abort the request; fail-open plugin failures are logged and ignored.
func (m *Manager) RunBefore(ctx context.Context, pctx *Context) error {
	m.mu.RLock()
	plugins := m.before
	m.mu.RUnlock()
	for _, p := range plugins {
		err := m.executePlugin(ctx, p, pctx, string(StageBeforeRequest))
		if failureErr := handlePluginFailure(p, StageBeforeRequest, pctx, err); failureErr != nil {
			return failureErr
		}
		if pctx.Skip {
			break
		}
	}
	return nil
}

// RunAfter executes all after-request plugins. Fail-closed plugin errors or
// rejections abort the response; fail-open plugin failures are logged and ignored.
func (m *Manager) RunAfter(ctx context.Context, pctx *Context) error {
	m.mu.RLock()
	plugins := m.after
	m.mu.RUnlock()
	for _, p := range plugins {
		err := m.executePlugin(ctx, p, pctx, string(StageAfterRequest))
		if failureErr := handlePluginFailure(p, StageAfterRequest, pctx, err); failureErr != nil {
			return failureErr
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
			slog.Default().Warn("on-error plugin error", "plugin", p.Name(), "error", err)
		}
	}
}

// executePlugin runs a single plugin under a child span opened through the
// observability seam. When the request carries a root span (pctx.Span) the
// child records the plugin name/kind/stage plus the rejection or error
// outcome; error messages are redacted by the seam per the configured
// privacy level. With the NoOp provider — or when no root span is set — the
// child is a no-op and adds effectively zero overhead.
func (m *Manager) executePlugin(ctx context.Context, p Plugin, pctx *Context, stage string) (err error) {
	var span observability.Span
	if pctx.Span != nil {
		ctx, span = pctx.Span.StartChild(ctx, "plugin."+stage+"."+p.Name(), observability.SpanKindInternal)
		span.SetAttribute(observability.AttrFerroPluginName, p.Name())
		span.SetAttribute(observability.AttrFerroPluginKind, string(p.Type()))
		span.SetAttribute(observability.AttrFerroPluginStage, stage)
		defer span.End()
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			stack := debug.Stack()
			err = fmt.Errorf("plugin %s panicked at %s", p.Name(), stage)
			slog.Default().Error("plugin panicked",
				"plugin", p.Name(),
				"stage", stage,
				"panic", recovered,
				"stack", string(stack),
			)
			if span != nil {
				span.SetAttribute(observability.AttrFerroPluginOutcome, "error")
				span.SetError(err)
			}
		}
	}()

	err = p.Execute(ctx, pctx)

	if span != nil {
		switch {
		case pctx.Reject:
			span.SetAttribute(observability.AttrFerroPluginOutcome, "rejected")
			if pctx.Reason != "" {
				span.SetAttribute(observability.AttrFerroPluginReason, pctx.Reason)
			}
		case err != nil:
			span.SetAttribute(observability.AttrFerroPluginOutcome, "error")
			span.SetError(err)
		default:
			span.SetAttribute(observability.AttrFerroPluginOutcome, "ok")
		}
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
		slog.Default().Warn("deferred plugin close failed", "error", err)
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
