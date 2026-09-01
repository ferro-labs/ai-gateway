package otel

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/ferro-labs/ai-gateway/observability"
)

// stubExporter records every event it is handed. It implements Exporter and
// nothing more: an exporter written before attempt events existed.
type stubExporter struct {
	name string
	mu   sync.Mutex
	got  []observability.Event
}

func (s *stubExporter) Name() string                               { return s.name }
func (s *stubExporter) Init(context.Context, map[string]any) error { return nil }
func (s *stubExporter) Shutdown(context.Context) error             { return nil }

func (s *stubExporter) Export(_ context.Context, evt observability.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, evt)
	return nil
}

func (s *stubExporter) subjects() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.got))
	for _, evt := range s.got {
		out = append(out, evt.Subject)
	}
	return out
}

// attemptStubExporter is a stubExporter that asks for attempt events.
type attemptStubExporter struct{ *stubExporter }

func (attemptStubExporter) ExportsRoutingAttempts() bool { return true }

func TestRoutingAttemptsEnabled_FollowsExporterOptIn(t *testing.T) {
	for _, tc := range []struct {
		name      string
		exporters []observability.Exporter
		want      bool
	}{
		{name: "no exporters", want: false},
		{name: "no exporter opts in", exporters: []observability.Exporter{&stubExporter{name: "plain"}}, want: false},
		{name: "one exporter opts in", exporters: []observability.Exporter{&stubExporter{name: "plain"}, attemptStubExporter{&stubExporter{name: "attempts"}}}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := newTestProvider(t)
			p.AttachExporters(tc.exporters)
			t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

			if got := p.RoutingAttemptsEnabled(); got != tc.want {
				t.Errorf("RoutingAttemptsEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRecordEvent_AttemptEventsReachOnlyExportersThatOptedIn(t *testing.T) {
	p, _ := newTestProvider(t)
	plain := &stubExporter{name: "plain"}
	wantsAttempts := attemptStubExporter{&stubExporter{name: "attempts"}}
	p.AttachExporters([]observability.Exporter{plain, wantsAttempts})

	const completed = "gateway.request.completed"
	p.RecordEvent(context.Background(), observability.Event{Subject: observability.SubjectRoutingAttempt})
	p.RecordEvent(context.Background(), observability.Event{Subject: completed})
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	if got := plain.subjects(); !slices.Equal(got, []string{completed}) {
		t.Errorf("exporter without opt-in received %v, want only the terminal event", got)
	}
	if got := wantsAttempts.subjects(); !slices.Equal(got, []string{observability.SubjectRoutingAttempt, completed}) {
		t.Errorf("opted-in exporter received %v, want the attempt and the terminal event", got)
	}
}
