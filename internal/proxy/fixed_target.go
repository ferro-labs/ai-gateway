package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/apierror"
	"github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/internal/streamio"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
)

// forwardFixedTarget transparently reverse-proxies r to one fixed upstream — the
// API root `target` with `authHeaders` injected — and streams the response back.
// It is the shared core of the surfaces that forward to a single configured
// backend rather than routing by model (Files/Batches and the Responses id
// sub-routes), and it reuses the /v1/* proxy's security machinery: the gateway's
// own /v1 prefix is stripped so the operation hangs beneath the root, the client
// credential is replaced by the provider's, an upstream that echoes the injected
// credential is redacted (bounded by sanitizeScanBudget), and the streaming idle
// bound replaces the WriteTimeout that WrapResponseWriter clears. The caller has
// already validated the path (unsafeProxyPath) and resolved target/authHeaders.
//
// wrapBody, when non-nil, wraps the response body just before it streams to the
// client — used by the Responses surface to tee usage out of the body; a nil
// value forwards the body untouched.
//
// It returns the upstream status (0 if the upstream was never reached) and the
// forward error (non-nil only when the upstream could not be reached or read;
// the ErrorHandler has already written the client's response in that case). A
// governed caller uses these to score the circuit breaker and record the
// outcome; a straight caller (Files/Batches) ignores them.
func forwardFixedTarget(w http.ResponseWriter, r *http.Request, target *url.URL, authHeaders map[string]string, providerName string, wrapBody func(*http.Response)) (upstreamStatus int, forwardErr error) {
	secrets := injectedSecrets(authHeaders)

	upstreamCtx, cancelUpstream := context.WithCancel(r.Context())
	defer cancelUpstream()
	r = r.WithContext(upstreamCtx)

	proxy := &httputil.ReverseProxy{
		Transport:     httpclient.SharedStreamingTransport(),
		FlushInterval: proxyFlushInterval,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Path = strings.TrimPrefix(pr.Out.URL.Path, "/v1")
			if pr.Out.URL.RawPath != "" {
				pr.Out.URL.RawPath = strings.TrimPrefix(pr.Out.URL.RawPath, "/v1")
			}
			pr.SetURL(target)
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Del("X-Provider")
			for k, v := range authHeaders {
				pr.Out.Header.Set(k, v)
			}
			pr.SetXForwarded()
		},
		ModifyResponse: func(resp *http.Response) error {
			upstreamStatus = resp.StatusCode
			resp.Header.Set("X-Gateway-Provider", providerName)
			scanTimer := time.AfterFunc(sanitizeScanBudget, cancelUpstream)
			sanitizeErr := sanitizeResponse(resp, secrets)
			scanTimer.Stop()
			if sanitizeErr != nil {
				return sanitizeErr
			}
			if wrapBody != nil {
				wrapBody(resp)
			}
			resp.Body = streamio.NewIdleReadCloser(resp.Body, streamio.IdleTimeout(), cancelUpstream)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			forwardErr = err
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				apierror.WriteOpenAI(w, http.StatusRequestEntityTooLarge, "request body too large", "invalid_request_error", "request_too_large")
				return
			}
			// providerName comes from the configured registry, not user input.
			logger.Default().Error("fixed-target proxy upstream error", "provider", providerName, "error", err)
			apierror.WriteOpenAI(w, http.StatusBadGateway, "upstream connection failed", "server_error", "upstream_error")
		},
	}

	proxy.ServeHTTP(streamio.WrapResponseWriter(w), r)
	return upstreamStatus, forwardErr
}
