package observability

import (
	"context"
	"errors"
	"testing"
)

func TestNoOpProviderImplementsInterface(t *testing.T) {
	p := Provider(NoOp())
	if p == nil {
		t.Fatal("expected non-nil NoOp provider")
	}
}

func TestNoOpSpanLifecycle(t *testing.T) {
	p := NoOp()
	ctx, span := p.StartRequestSpan(context.Background(), RequestAttrs{
		System:       "openai",
		Operation:    "chat",
		RequestModel: "gpt-4o",
	})
	if ctx == nil {
		t.Fatal("expected non-nil context")
	}

	span.SetAttribute(AttrGenAISystem, "openai")
	span.SetTokens(1, 2, 3)
	span.SetCost(CostBreakdown{TotalUSD: 0.1, ModelFound: true})
	span.SetError(errors.New("test"))
	span.SetStreamTimings(100, 200)

	childCtx, child := span.StartChild(ctx, "provider.call", SpanKindClient)
	if childCtx == nil {
		t.Fatal("expected non-nil child context")
	}
	child.End()
	span.End()

	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}
}

func TestNoOpRecordEvent(_ *testing.T) {
	p := NoOp()
	// Must not panic with empty event.
	p.RecordEvent(context.Background(), Event{})
}

func TestRegisterExporter(t *testing.T) {
	t.Cleanup(resetRegistryForTest)

	RegisterExporter("test", func() Exporter { return testExporter{} })

	if _, ok := LookupExporter("test"); !ok {
		t.Fatal("expected exporter to be registered")
	}
	names := RegisteredExporters()
	if len(names) != 1 || names[0] != "test" {
		t.Fatalf("unexpected RegisteredExporters: %v", names)
	}
}

func TestLookupExporterMissing(t *testing.T) {
	t.Cleanup(resetRegistryForTest)
	if _, ok := LookupExporter("nope"); ok {
		t.Fatal("expected lookup miss")
	}
}

func TestRegisterExporterDuplicatePanics(t *testing.T) {
	t.Cleanup(resetRegistryForTest)

	RegisterExporter("dup", func() Exporter { return testExporter{} })

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on duplicate registration")
		}
	}()
	RegisterExporter("dup", func() Exporter { return testExporter{} })
}

func TestRegisterExporterEmptyNamePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on empty name")
		}
	}()
	RegisterExporter("", func() Exporter { return testExporter{} })
}

func TestRegisterExporterNilFactoryPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil factory")
		}
	}()
	RegisterExporter("foo", nil)
}

func TestSchemaVersionIsSet(t *testing.T) {
	if SchemaVersion == "" {
		t.Fatal("SchemaVersion must not be empty")
	}
}

// testExporter is a minimal Exporter implementation used in unit tests.
type testExporter struct{}

func (testExporter) Name() string                                   { return "test" }
func (testExporter) Init(_ context.Context, _ map[string]any) error { return nil }
func (testExporter) Export(_ context.Context, _ Event) error        { return nil }
func (testExporter) Shutdown(_ context.Context) error               { return nil }

// Compile-time guard.
var _ Exporter = (*testExporter)(nil)

// captureExporter records calls for assertion in tests.
type captureExporter struct {
	initCalled     bool
	initCfg        map[string]any
	exported       []Event
	shutdownCalled bool
}

func (e *captureExporter) Name() string { return "capture" }
func (e *captureExporter) Init(_ context.Context, cfg map[string]any) error {
	e.initCalled = true
	e.initCfg = cfg
	return nil
}
func (e *captureExporter) Export(_ context.Context, evt Event) error {
	e.exported = append(e.exported, evt)
	return nil
}
func (e *captureExporter) Shutdown(_ context.Context) error {
	e.shutdownCalled = true
	return nil
}

var _ Exporter = (*captureExporter)(nil)

func TestExporterRegistryRoundTrip(t *testing.T) {
	t.Cleanup(resetRegistryForTest)

	capture := &captureExporter{}
	RegisterExporter("capture", func() Exporter { return capture })

	factory, ok := LookupExporter("capture")
	if !ok {
		t.Fatal("expected exporter to be found after registration")
	}

	ex := factory()
	initCfg := map[string]any{"api_key": "tok123"}
	if err := ex.Init(context.Background(), initCfg); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}

	evt := Event{Subject: "gateway.request.completed", Provider: "openai", Model: "gpt-4o", Status: 200}
	if err := ex.Export(context.Background(), evt); err != nil {
		t.Fatalf("Export returned error: %v", err)
	}
	if err := ex.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}

	if !capture.initCalled {
		t.Error("Init was not called on the exporter")
	}
	if capture.initCfg["api_key"] != "tok123" {
		t.Errorf("Init cfg mismatch: got %v", capture.initCfg)
	}
	if len(capture.exported) != 1 || capture.exported[0].Subject != "gateway.request.completed" {
		t.Errorf("unexpected exported events: %v", capture.exported)
	}
	if !capture.shutdownCalled {
		t.Error("Shutdown was not called on the exporter")
	}
}

func TestEventRecordingProviderInterface(t *testing.T) {
	// NoOp must NOT implement EventRecordingProvider.
	noopProv := NoOp()
	if _, ok := noopProv.(EventRecordingProvider); ok {
		t.Error("NoOp should NOT implement EventRecordingProvider")
	}
}
