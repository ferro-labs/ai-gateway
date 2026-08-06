// Package logger provides a request-logger plugin that records each LLM
// request and response to standard output. Register it with a blank import:
//
//	_ "github.com/ferro-labs/ai-gateway/plugin/logger"
package logger

import (
	"context"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/redact"
	"github.com/ferro-labs/ai-gateway/internal/requestlog"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
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
	logLevel logger.Level
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
	l.logLevel = logger.LevelInfo
	l.writer = requestlog.NoopWriter{}
	l.redactor = redact.DefaultRedactor()
	if level, ok := config["level"].(string); ok {
		switch level {
		case "debug":
			l.logLevel = logger.LevelDebug
		case "warn":
			l.logLevel = logger.LevelWarn
		case "error":
			l.logLevel = logger.LevelError
		}
	}

	if _, ok := config["dsn"]; ok {
		logger.Default().Warn("request-logger: the dsn option is ignored; set the request log store with REQUEST_LOG_STORE_DSN")
	}
	if _, ok := config["backend"]; ok {
		logger.Default().Warn("request-logger: the backend option is ignored; set the request log store with REQUEST_LOG_STORE_BACKEND")
	}

	persist, _ := config["persist"].(bool)
	switch {
	case !persist:
		// stdout only; l.writer stays NoopWriter.
	case l.shared != nil:
		l.writer = l.shared
	default:
		logger.Default().Warn("request-logger: persist is set but no request log store is configured; set REQUEST_LOG_STORE_BACKEND to persist logs")
	}
	return nil
}

// measured returns a pointer to value when it was actually measured, and nil
// otherwise, so the stored row distinguishes "not measured" from "zero". A
// request the catalog cannot price has an unknown cost, not a free one, and
// persisting 0.0 would silently understate every spend figure derived from it.
func measured(value float64, ok bool) *float64 {
	if !ok {
		return nil
	}
	return &value
}

// apiKeyID returns the opaque identifier of the credential the request was
// served under, or "" when it carried none.
//
// It reads the same Metadata slot the per-key governance plugins bucket on, so
// the row is attributed to exactly the identity the rate limiter and the budget
// charged. The gateway puts the key's ID there, never the secret.
func apiKeyID(pctx *plugin.Context) string {
	id, _ := pctx.Metadata["api_key"].(string)
	return id
}

// persist appends one entry to the request-log store, reporting a rejected
// write instead of dropping it. It reports whether the row reached the store.
//
// The failure never reaches the caller: the framework reads an error from a
// plugin as the plugin itself breaking, and a logging plugin is failed open
// precisely so a bookkeeping problem cannot cost a completed LLM call. But a
// store that is down, full, or out of disk otherwise loses the persisted trail
// with nothing anywhere to say so, leaving the operator to trust a request log
// that is quietly incomplete. The warning is what makes the gap visible; the
// stage says which of the three writes was lost.
func (l *RequestLogger) persist(ctx context.Context, log *logger.Logger, entry requestlog.Entry) bool {
	if err := l.writer.Write(ctx, entry); err != nil {
		log.Warn("request log write failed; the persisted request log is incomplete",
			"stage", entry.Stage,
			"error", err,
		)
		return false
	}
	return true
}

// metaTerminalRow is the Metadata slot marking that this request already has a
// terminal row in the store.
//
// It lives in the per-request Metadata rather than on the plugin, because one
// RequestLogger instance serves every stage of every concurrent request: a
// field on the receiver would be one request's answer given to another's. The
// key is namespaced for the same reason the map is documented as shared.
const metaTerminalRow = "request-logger.terminal_row"

// markTerminalRow records that this request's terminal row is now in the store.
func markTerminalRow(pctx *plugin.Context) {
	if pctx.Metadata == nil {
		return
	}
	pctx.Metadata[metaTerminalRow] = true
}

// hasTerminalRow reports whether this request already wrote a terminal row.
func hasTerminalRow(pctx *plugin.Context) bool {
	written, _ := pctx.Metadata[metaTerminalRow].(bool)
	return written
}

// recordLateFailure attaches a failure to the terminal row this request already
// wrote, and reports whether it landed.
//
// A request that completed and then failed — the provider answered, this row
// was written, and an after_request plugin behind the logger broke — reaches
// on_error with a terminal row already in the store. Writing a second one there
// gives one request two terminal rows, which the default listing shows twice
// and every Stats figure counts twice, in the middle of the failure an operator
// is reading them to understand. The failure still has to be recorded, so it is
// recorded on the row that already exists.
//
// It returns false when there is nothing to annotate — a store that cannot, or
// a row that has gone since — and the caller writes the on_error row as before.
// A lost failure would be the worse outcome of the two.
func (l *RequestLogger) recordLateFailure(ctx context.Context, log *logger.Logger, traceID, message string) bool {
	annotator, ok := l.writer.(requestlog.ErrorAnnotator)
	if !ok {
		return false
	}
	annotated, err := annotator.AnnotateError(ctx, traceID, string(plugin.StageAfterRequest), message)
	if err != nil {
		log.Warn("request log annotation failed; the recorded request does not name its failure",
			"error", err,
		)
		return false
	}
	return annotated
}

// Execute runs the plugin logic for the current request context.
//
// A request served from cache is logged like any other: it was served, so it
// belongs in the trail. Unlike a plugin that bills for provider work, a plugin that
// records that a request happened must not consult SkipProvider — suppressing the
// row would lose the request from every audit surface the operator has. Its row
// carries the tokens that were served and the credential that consumed them,
// paired with the zero cost the gateway hands it: usage and cost are different
// facts, and a cache hit is where they come apart.
func (l *RequestLogger) Execute(ctx context.Context, pctx *plugin.Context) error {
	log := logger.Ctx(ctx)
	if pctx.Stage == plugin.StageBeforeRequest && pctx.Request != nil {
		now := time.Now().UTC()
		log.Log(ctx, l.logLevel, "gateway request",
			"model", pctx.Request.Model,
			"messages", len(pctx.Request.Messages),
			"stream", pctx.Request.Stream,
			"timestamp", now.Format(time.RFC3339),
		)
		l.persist(ctx, log, requestlog.Entry{
			TraceID:   logger.TraceIDFromContext(ctx),
			Stage:     string(plugin.StageBeforeRequest),
			Model:     pctx.Request.Model,
			APIKeyID:  apiKeyID(pctx),
			CreatedAt: now,
		})
	}

	if pctx.Stage == plugin.StageAfterRequest && pctx.Response != nil {
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
		wrote := l.persist(ctx, log, requestlog.Entry{
			TraceID: logger.TraceIDFromContext(ctx),
			Stage:   string(plugin.StageAfterRequest),
			Model:   pctx.Response.Model,
			// The provider the response came from, which for a response served
			// from cache is the provider that produced it originally — while
			// APIKeyID is whoever consumed it this time. The two answer different
			// questions and a cache hit is where they diverge.
			Provider:         pctx.Response.Provider,
			APIKeyID:         apiKeyID(pctx),
			PromptTokens:     pctx.Response.Usage.PromptTokens,
			CompletionTokens: pctx.Response.Usage.CompletionTokens,
			TotalTokens:      pctx.Response.Usage.TotalTokens,
			CreatedAt:        now,
			DurationMs:       measured(pctx.Measurements.DurationMs, true),
			TTFTMs:           measured(pctx.Measurements.TTFTMs, pctx.Measurements.HasTTFT),
			CostUSD:          measured(pctx.Measurements.CostUSD, pctx.Measurements.HasCost),
		})
		if wrote {
			markTerminalRow(pctx)
		}
	}

	if pctx.Stage == plugin.StageOnError && pctx.Error != nil {
		// on_error stage — route error text through the redactor before logging
		// so provider API keys embedded in upstream error messages are not persisted.
		now := time.Now().UTC()
		model := ""
		if pctx.Request != nil {
			model = pctx.Request.Model
		}
		errMsg := l.redactor.Redact(pctx.Error.Error())
		log.Log(ctx, logger.LevelError, "gateway error",
			"model", model,
			"provider", pctx.Target,
			"error", errMsg,
			"timestamp", now.Format(time.RFC3339),
		)
		// A request that already has a terminal row gets its failure recorded on
		// that row instead of a second one. See recordLateFailure.
		if hasTerminalRow(pctx) && l.recordLateFailure(ctx, log, logger.TraceIDFromContext(ctx), errMsg) {
			return nil
		}
		l.persist(ctx, log, requestlog.Entry{
			TraceID: logger.TraceIDFromContext(ctx),
			Stage:   string(plugin.StageOnError),
			Model:   model,
			// There is no response on this path, so the provider comes from the
			// routing target instead — the last one attempted. A failure row that
			// cannot name what failed is most of the row's value missing, and it
			// is the same column the after_request row fills from the response.
			Provider:     pctx.Target,
			APIKeyID:     apiKeyID(pctx),
			ErrorMessage: errMsg,
			CreatedAt:    now,
			// A failed request still took time, and how long it took to fail is
			// worth as much as how long a success took. It has no cost: nothing
			// was billed, and no token count reached the catalog to price.
			DurationMs: measured(pctx.Measurements.DurationMs, pctx.Measurements.DurationMs > 0),
			TTFTMs:     measured(pctx.Measurements.TTFTMs, pctx.Measurements.HasTTFT),
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
