package transport

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func TestClientsPropagateTraceWithoutIdentityBaggage(t *testing.T) {
	previous := otel.GetTextMapPropagator()
	defer otel.SetTextMapPropagator(previous)
	propagator := propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})
	otel.SetTextMapPropagator(propagator)

	inbound := propagation.MapCarrier{
		"traceparent": "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		"tracestate":  "vendor=opaque",
		"baggage":     "user.id=alice,session.id=session-7",
	}
	ctx := propagator.Extract(t.Context(), inbound)
	manager := NewDefault()
	defer manager.CloseIdleConnections()
	manager.RegisterProvider("test", DefaultConfig())

	for name, client := range map[string]*http.Client{
		"default":   manager.DefaultClient(),
		"provider":  manager.ForProvider("test"),
		"streaming": manager.streamClient,
	} {
		t.Run(name, func(t *testing.T) {
			seen := make(chan http.Header, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen <- r.Header.Clone()
				w.WriteHeader(http.StatusOK)
			}))
			defer upstream.Close()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			headers := <-seen
			if got := headers.Get("baggage"); got != "" {
				t.Errorf("identity baggage reached provider: %q", got)
			}
			outCtx := propagation.TraceContext{}.Extract(t.Context(), propagation.HeaderCarrier(headers))
			if got, want := trace.SpanContextFromContext(outCtx).TraceID(), trace.SpanContextFromContext(ctx).TraceID(); got != want {
				t.Errorf("trace ID = %s, want %s", got, want)
			}
			if got := headers.Get("tracestate"); got != "vendor=opaque" {
				t.Errorf("tracestate = %q, want vendor=opaque", got)
			}
		})
	}
}
