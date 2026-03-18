package httpclient

import (
	"crypto/tls"
	"log/slog"
	"net/http"
	"net/http/httptrace"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/logging"
)

type tracingTransport struct {
	base http.RoundTripper
}

type outboundTrace struct {
	start time.Time

	dnsStart     time.Time
	connectStart time.Time
	tlsStart     time.Time

	dnsDuration     time.Duration
	connectDuration time.Duration
	tlsDuration     time.Duration
	firstByte       time.Duration

	reusedConn bool
	wasIdle    bool
	idleTime   time.Duration
}

func newTracingTransport(base http.RoundTripper) http.RoundTripper {
	return tracingTransport{base: base}
}

// UsesSharedTransport reports whether rt is wired to the package's shared
// transport policy, either directly or through the tracing wrapper.
func UsesSharedTransport(rt http.RoundTripper) bool {
	if rt == sharedTransport {
		return true
	}
	tracingRT, ok := rt.(tracingTransport)
	return ok && tracingRT.base == sharedTransport
}

func (t tracingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || !logging.Enabled(req.Context(), slog.LevelDebug) {
		return t.base.RoundTrip(req)
	}

	traceData := &outboundTrace{start: time.Now()}
	traceCtx := httptrace.WithClientTrace(req.Context(), traceData.clientTrace())
	clone := req.Clone(traceCtx)

	resp, err := t.base.RoundTrip(clone)
	traceData.log(clone, resp, err)
	return resp, err
}

func (t *outboundTrace) clientTrace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) {
			if t.dnsStart.IsZero() {
				t.dnsStart = time.Now()
			}
		},
		DNSDone: func(httptrace.DNSDoneInfo) {
			if !t.dnsStart.IsZero() && t.dnsDuration == 0 {
				t.dnsDuration = time.Since(t.dnsStart)
			}
		},
		ConnectStart: func(_, _ string) {
			if t.connectStart.IsZero() {
				t.connectStart = time.Now()
			}
		},
		ConnectDone: func(_, _ string, _ error) {
			if !t.connectStart.IsZero() && t.connectDuration == 0 {
				t.connectDuration = time.Since(t.connectStart)
			}
		},
		TLSHandshakeStart: func() {
			if t.tlsStart.IsZero() {
				t.tlsStart = time.Now()
			}
		},
		TLSHandshakeDone: func(_ tls.ConnectionState, _ error) {
			if !t.tlsStart.IsZero() && t.tlsDuration == 0 {
				t.tlsDuration = time.Since(t.tlsStart)
			}
		},
		GotConn: func(info httptrace.GotConnInfo) {
			t.reusedConn = info.Reused
			t.wasIdle = info.WasIdle
			t.idleTime = info.IdleTime
		},
		GotFirstResponseByte: func() {
			if t.firstByte == 0 {
				t.firstByte = time.Since(t.start)
			}
		},
	}
}

func (t *outboundTrace) log(req *http.Request, resp *http.Response, err error) {
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}

	attrs := []any{
		"method", req.Method,
		"scheme", req.URL.Scheme,
		"host", req.URL.Host,
		"path", req.URL.Path,
		"status", status,
		"reused_conn", t.reusedConn,
		"conn_was_idle", t.wasIdle,
		"conn_idle_ms", t.idleTime.Milliseconds(),
		"dns_ms", t.dnsDuration.Milliseconds(),
		"dial_ms", t.connectDuration.Milliseconds(),
		"tls_ms", t.tlsDuration.Milliseconds(),
		"ttfb_ms", t.firstByte.Milliseconds(),
		"total_ms", time.Since(t.start).Milliseconds(),
	}
	if err != nil {
		attrs = append(attrs, "error", err.Error())
	}

	logging.FromContext(req.Context()).Debug("outbound http trace", attrs...)
}
