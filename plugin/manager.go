package plugin

import (
	"context"
	"fmt"
	"log/slog"
)

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
	slog.Info("plugin registered", "name", p.Name(), "type", p.Type(), "stage", stage)
	return nil
}

// RunBefore executes all before-request plugins. Returns an error if a plugin
// rejects the request.
func (m *Manager) RunBefore(ctx context.Context, pctx *Context) error {
	for _, p := range m.before {
		if err := p.Execute(ctx, pctx); err != nil {
			return fmt.Errorf("plugin %s failed: %w", p.Name(), err)
		}
		if pctx.Reject {
			return fmt.Errorf("request rejected by %s: %s", p.Name(), pctx.Reason)
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
		if err := p.Execute(ctx, pctx); err != nil {
			slog.Warn("after-request plugin error", "plugin", p.Name(), "error", err)
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
		if err := p.Execute(ctx, pctx); err != nil {
			slog.Warn("on-error plugin error", "plugin", p.Name(), "error", err)
		}
	}
}

// HasPlugins returns true if any plugins are registered.
func (m *Manager) HasPlugins() bool {
	return len(m.before)+len(m.after)+len(m.onErr) > 0
}
