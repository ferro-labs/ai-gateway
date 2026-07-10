// Package logger provides a request-logger plugin that records each LLM
// request and response to standard output. Register it with a blank import:
//
//	_ "github.com/ferro-labs/ai-gateway/internal/plugins/logger"
package logger

import (
	"context"
	"log/slog"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/ferro-labs/ai-gateway/internal/redact"
	"github.com/ferro-labs/ai-gateway/internal/requestlog"
	"github.com/ferro-labs/ai-gateway/plugin"
)

func init() {
	plugin.RegisterFactory("request-logger", func() plugin.Plugin {
		return &RequestLogger{}
	})
}

// RequestLogger is a logging plugin that emits structured log entries
// for every request and response flowing through the gateway.
type RequestLogger struct {
	logLevel slog.Level
	writer   requestlog.Writer
	shared   requestlog.Writer
	redactor *redact.Redactor
}

// Name returns the plugin identifier.
func (l *RequestLogger) Name() string { return "request-logger" }

// Type returns the plugin lifecycle hook type.
func (l *RequestLogger) Type() plugin.PluginType { return plugin.TypeLogging }

// SetRequestLogWriter receives the shared request-log store the gateway builds
// from REQUEST_LOG_STORE_BACKEND / REQUEST_LOG_STORE_DSN. The gateway calls this
// before Init. The store is owned by the gateway, so Close does not touch it.
func (l *RequestLogger) SetRequestLogWriter(w requestlog.Writer) {
	l.shared = w
}

// Init configures the plugin from the provided options map.
//
// Persistence is directed at the shared request-log store, not a per-plugin
// database. `backend`/`dsn` here are obsolete — persistence targets and
// credentials are process configuration, set once via
// REQUEST_LOG_STORE_BACKEND / REQUEST_LOG_STORE_DSN — and are ignored with a
// warning so an operator running an old config learns where the setting moved.
func (l *RequestLogger) Init(config map[string]any) error {
	l.logLevel = slog.LevelInfo
	l.writer = requestlog.NoopWriter{}
	l.redactor = redact.DefaultRedactor()
	if level, ok := config["level"].(string); ok {
		switch level {
		case "debug":
			l.logLevel = slog.LevelDebug
		case "warn":
			l.logLevel = slog.LevelWarn
		case "error":
			l.logLevel = slog.LevelError
		}
	}

	if _, ok := config["dsn"]; ok {
		slog.Warn("request-logger: the dsn option is ignored; set the request log store with REQUEST_LOG_STORE_DSN")
	}
	if _, ok := config["backend"]; ok {
		slog.Warn("request-logger: the backend option is ignored; set the request log store with REQUEST_LOG_STORE_BACKEND")
	}

	persist, _ := config["persist"].(bool)
	switch {
	case !persist:
		// stdout only; l.writer stays NoopWriter.
	case l.shared != nil:
		l.writer = l.shared
	default:
		slog.Warn("request-logger: persist is set but no request log store is configured; set REQUEST_LOG_STORE_BACKEND to persist logs")
	}
	return nil
}

// Execute runs the plugin logic for the current request context.
func (l *RequestLogger) Execute(ctx context.Context, pctx *plugin.Context) error {
	log := logging.FromContext(ctx)
	if pctx.Request != nil && pctx.Response == nil && pctx.Error == nil {
		// before_request stage
		now := time.Now().UTC()
		log.Log(ctx, l.logLevel, "gateway request",
			"model", pctx.Request.Model,
			"messages", len(pctx.Request.Messages),
			"stream", pctx.Request.Stream,
			"timestamp", now.Format(time.RFC3339),
		)
		_ = l.writer.Write(ctx, requestlog.Entry{
			TraceID:   logging.TraceIDFromContext(ctx),
			Stage:     string(plugin.StageBeforeRequest),
			Model:     pctx.Request.Model,
			CreatedAt: now,
		})
	}

	if pctx.Response != nil {
		// after_request stage
		now := time.Now().UTC()
		log.Log(ctx, l.logLevel, "gateway response",
			"model", pctx.Response.Model,
			"provider", pctx.Response.Provider,
			"prompt_tokens", pctx.Response.Usage.PromptTokens,
			"completion_tokens", pctx.Response.Usage.CompletionTokens,
			"total_tokens", pctx.Response.Usage.TotalTokens,
			"choices", len(pctx.Response.Choices),
			"timestamp", now.Format(time.RFC3339),
		)
		_ = l.writer.Write(ctx, requestlog.Entry{
			TraceID:          logging.TraceIDFromContext(ctx),
			Stage:            string(plugin.StageAfterRequest),
			Model:            pctx.Response.Model,
			Provider:         pctx.Response.Provider,
			PromptTokens:     pctx.Response.Usage.PromptTokens,
			CompletionTokens: pctx.Response.Usage.CompletionTokens,
			TotalTokens:      pctx.Response.Usage.TotalTokens,
			CreatedAt:        now,
		})
	}

	if pctx.Error != nil {
		// on_error stage — route error text through the redactor before logging
		// so provider API keys embedded in upstream error messages are not persisted.
		now := time.Now().UTC()
		model := ""
		if pctx.Request != nil {
			model = pctx.Request.Model
		}
		errMsg := l.redactor.Redact(pctx.Error.Error())
		log.Log(ctx, slog.LevelError, "gateway error",
			"model", model,
			"error", errMsg,
			"timestamp", now.Format(time.RFC3339),
		)
		_ = l.writer.Write(ctx, requestlog.Entry{
			TraceID:      logging.TraceIDFromContext(ctx),
			Stage:        string(plugin.StageOnError),
			Model:        model,
			ErrorMessage: errMsg,
			CreatedAt:    now,
		})
	}

	return nil
}

// Close is a no-op. The request-log store the plugin writes to is owned by the
// gateway, which closes it on shutdown; closing it here would break the admin
// log reader that shares the same store.
func (l *RequestLogger) Close() error {
	return nil
}
