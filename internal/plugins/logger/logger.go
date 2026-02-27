// Package logger provides a request-logger plugin that records each LLM
// request and response to standard output. Register it with a blank import:
//
//	_ "github.com/ferro-labs/ai-gateway/internal/plugins/logger"
package logger

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/logging"
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
}

// Name returns the plugin identifier.
func (l *RequestLogger) Name() string { return "request-logger" }

// Type returns the plugin lifecycle hook type.
func (l *RequestLogger) Type() plugin.PluginType { return plugin.TypeLogging }

// Init configures the plugin from the provided options map.
func (l *RequestLogger) Init(config map[string]interface{}) error {
	l.logLevel = slog.LevelInfo
	l.writer = requestlog.NoopWriter{}
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

	persist, _ := config["persist"].(bool)
	if persist {
		backend, _ := config["backend"].(string)
		dsn, _ := config["dsn"].(string)
		switch strings.ToLower(strings.TrimSpace(backend)) {
		case "sqlite", "":
			writer, err := requestlog.NewSQLiteWriter(dsn)
			if err != nil {
				return err
			}
			l.writer = writer
		case "postgres", "postgresql":
			writer, err := requestlog.NewPostgresWriter(dsn)
			if err != nil {
				return err
			}
			l.writer = writer
		default:
			return fmt.Errorf("unsupported request log backend %q", backend)
		}
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
		// on_error stage
		now := time.Now().UTC()
		log.Log(ctx, slog.LevelError, "gateway error",
			"model", pctx.Request.Model,
			"error", pctx.Error.Error(),
			"timestamp", now.Format(time.RFC3339),
		)
		model := ""
		if pctx.Request != nil {
			model = pctx.Request.Model
		}
		_ = l.writer.Write(ctx, requestlog.Entry{
			TraceID:      logging.TraceIDFromContext(ctx),
			Stage:        string(plugin.StageOnError),
			Model:        model,
			ErrorMessage: pctx.Error.Error(),
			CreatedAt:    now,
		})
	}

	return nil
}
