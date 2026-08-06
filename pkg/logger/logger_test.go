package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func decode(t *testing.T, line string) map[string]any {
	t.Helper()
	m := map[string]any{}
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("log line is not JSON: %v -- %q", err, line)
	}
	return m
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, ln := range strings.Split(strings.TrimSpace(s), "\n") {
		if strings.TrimSpace(ln) != "" {
			out = append(out, ln)
		}
	}
	return out
}

func TestParseLevel(t *testing.T) {
	cases := []struct {
		in   string
		want Level
		ok   bool
	}{
		{"debug", LevelDebug, true},
		{"info", LevelInfo, true},
		{"", LevelInfo, true},
		{"WARN", LevelWarn, true},
		{"warning", LevelWarn, true},
		{"error", LevelError, true},
		{"  Debug  ", LevelDebug, true},
		{"bogus", LevelInfo, false},
	}
	for _, c := range cases {
		got, ok := ParseLevel(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("ParseLevel(%q) = (%v, %v), want (%v, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestNew_DefaultLevelIsInfo(t *testing.T) {
	var buf bytes.Buffer
	l := New(Options{Output: &buf}) // empty Level -> info
	l.Debug("suppressed")
	l.Info("kept", "k", "v")

	lines := nonEmptyLines(buf.String())
	if len(lines) != 1 {
		t.Fatalf("want 1 line (debug suppressed at info default), got %d: %q", len(lines), buf.String())
	}
	m := decode(t, lines[0])
	if m["level"] != "INFO" || m["msg"] != "kept" || m["k"] != "v" {
		t.Errorf("unexpected line: %v", m)
	}
	if _, ok := m["time"]; !ok {
		t.Errorf("structured line missing time: %v", m)
	}
}

func TestNew_ExplicitLevelFilters(t *testing.T) {
	var buf bytes.Buffer
	l := New(Options{Level: "error", Output: &buf})
	l.Warn("suppressed")
	l.Error("kept")

	lines := nonEmptyLines(buf.String())
	if len(lines) != 1 || decode(t, lines[0])["level"] != "ERROR" {
		t.Fatalf("error level not applied: %q", buf.String())
	}
}

func TestWith_AddsFields(t *testing.T) {
	var buf bytes.Buffer
	l := New(Options{Output: &buf}).With("service", "gateway").WithComponent("mcp")
	l.Info("hi")

	m := decode(t, strings.TrimSpace(buf.String()))
	if m["service"] != "gateway" || m["component"] != "mcp" {
		t.Errorf("fields missing: %v", m)
	}
}

func TestLog_RuntimeLevel(t *testing.T) {
	var buf bytes.Buffer
	l := New(Options{Level: "warn", Output: &buf})
	l.Log(context.Background(), LevelInfo, "suppressed")
	l.Log(context.Background(), LevelError, "kept")

	lines := nonEmptyLines(buf.String())
	if len(lines) != 1 || decode(t, lines[0])["level"] != "ERROR" {
		t.Fatalf("Log level gating wrong: %q", buf.String())
	}
}

func TestEnabled(t *testing.T) {
	l := New(Options{Level: "warn", Output: io.Discard})
	if l.Enabled(context.Background(), LevelInfo) {
		t.Error("info should be disabled at warn level")
	}
	if !l.Enabled(context.Background(), LevelError) {
		t.Error("error should be enabled at warn level")
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv(EnvLevel, "error")
	o := FromEnv()
	o.Output = new(bytes.Buffer)
	l := New(o)
	if l.Enabled(context.Background(), LevelWarn) {
		t.Error("LOG_LEVEL=error should disable warn")
	}
}

func TestDefault_NotNil(t *testing.T) {
	if Default() == nil {
		t.Fatal("Default() must never be nil")
	}
}

func TestSetDefault(t *testing.T) {
	// SetDefault also mirrors into slog.SetDefault, mutating the process-global
	// stdlib default; restore both so this test leaves nothing behind.
	prev := Default()
	prevSlog := slog.Default()
	t.Cleanup(func() { SetDefault(prev); slog.SetDefault(prevSlog) })

	replacement := New(Options{Level: "debug", Output: io.Discard})
	SetDefault(replacement)
	if Default() != replacement {
		t.Fatal("SetDefault did not install the logger")
	}
	if slog.Default() != replacement.log {
		t.Fatal("SetDefault did not mirror into slog.Default")
	}
}
