package logger

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

func TestRequestLogger_Init(t *testing.T) {
	t.Run("default level", func(t *testing.T) {
		l := &RequestLogger{}
		if err := l.Init(map[string]interface{}{}); err != nil {
			t.Fatalf("Init failed: %v", err)
		}
		if l.logLevel != slog.LevelInfo {
			t.Errorf("expected default level Info, got %v", l.logLevel)
		}
	})

	t.Run("debug level", func(t *testing.T) {
		l := &RequestLogger{}
		if err := l.Init(map[string]interface{}{"level": "debug"}); err != nil {
			t.Fatalf("Init failed: %v", err)
		}
		if l.logLevel != slog.LevelDebug {
			t.Errorf("expected Debug level, got %v", l.logLevel)
		}
	})
}

func TestRequestLogger_ExecuteRequest(t *testing.T) {
	l := &RequestLogger{}
	if err := l.Init(map[string]interface{}{}); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	req := &providers.Request{
		Model: "gpt-4",
		Messages: []providers.Message{
			{Role: "user", Content: "hello"},
		},
	}
	pctx := plugin.NewContext(req)

	if err := l.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
}

func TestRequestLogger_ExecuteResponse(t *testing.T) {
	l := &RequestLogger{}
	if err := l.Init(map[string]interface{}{}); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	req := &providers.Request{
		Model: "gpt-4",
		Messages: []providers.Message{
			{Role: "user", Content: "hello"},
		},
	}
	pctx := plugin.NewContext(req)
	pctx.Response = &providers.Response{
		Model:    "gpt-4",
		Provider: "openai",
		Usage:    providers.Usage{PromptTokens: 5, CompletionTokens: 10, TotalTokens: 15},
		Choices: []providers.Choice{
			{Index: 0, Message: providers.Message{Role: "assistant", Content: "hi"}, FinishReason: "stop"},
		},
	}

	if err := l.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
}

func TestRequestLogger_ExecuteError(t *testing.T) {
	l := &RequestLogger{}
	if err := l.Init(map[string]interface{}{}); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	req := &providers.Request{
		Model: "gpt-4",
		Messages: []providers.Message{
			{Role: "user", Content: "hello"},
		},
	}
	pctx := plugin.NewContext(req)
	pctx.Error = errors.New("provider timeout")

	if err := l.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
}

func TestRequestLogger_ExecuteErrorWithoutRequest(t *testing.T) {
	l := &RequestLogger{}
	if err := l.Init(map[string]interface{}{}); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	pctx := plugin.NewContext(nil)
	pctx.Error = errors.New("provider timeout")

	if err := l.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
}

func TestRequestLogger_Name(t *testing.T) {
	l := &RequestLogger{}
	if l.Name() != "request-logger" {
		t.Errorf("Name() = %q, want %q", l.Name(), "request-logger")
	}
}

func TestRequestLogger_Type(t *testing.T) {
	l := &RequestLogger{}
	if l.Type() != plugin.TypeLogging {
		t.Errorf("Type() = %v, want TypeLogging", l.Type())
	}
}

func TestRequestLogger_Init_WarnLevel(t *testing.T) {
	l := &RequestLogger{}
	if err := l.Init(map[string]interface{}{"level": "warn"}); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if l.logLevel != slog.LevelWarn {
		t.Errorf("expected Warn level, got %v", l.logLevel)
	}
}

func TestRequestLogger_Init_ErrorLevel(t *testing.T) {
	l := &RequestLogger{}
	if err := l.Init(map[string]interface{}{"level": "error"}); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if l.logLevel != slog.LevelError {
		t.Errorf("expected Error level, got %v", l.logLevel)
	}
}

func TestRequestLogger_Init_UnsupportedBackend(t *testing.T) {
	l := &RequestLogger{}
	err := l.Init(map[string]interface{}{
		"persist": true,
		"backend": "cassandra",
		"dsn":     "",
	})
	if err == nil {
		t.Error("expected error for unsupported backend")
	}
}
