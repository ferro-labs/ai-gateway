package proxy

import (
	"net/http"
	"net/url"

	"github.com/ferro-labs/ai-gateway/internal/apierror"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
	"github.com/ferro-labs/ai-gateway/providers"
)

// BatchSource resolves the single backend that serves the Files and Batch
// surfaces. *Gateway implements it: BatchTarget names the configured target and
// Get resolves it to a provider.
type BatchSource interface {
	Get(name string) (providers.Provider, bool)
	BatchTarget() string
}

// BatchHandler returns an http.HandlerFunc that transparently forwards the Files
// (/v1/files*) and Batch (/v1/batches*) endpoints to the one configured batch
// target, injecting that provider's credential.
//
// Unlike the /v1/* pass-through, this surface does NOT resolve a provider per
// request: files and batches carry no model, and a bare file/batch id is an
// opaque, provider-scoped string with no routing hint. A single configured
// backend serves them all, so an id minted on POST /v1/files resolves against
// the same backend on every later retrieve, cancel or download — no gateway
// state, native ids preserved. When no batch_target is configured the surface is
// off and every route answers 501.
//
// It reuses the /v1/* proxy's security machinery: path-traversal refusal before
// any credential is installed, response credential redaction bounded by
// sanitizeScanBudget, and the streaming idle bound that stands in for the
// cleared WriteTimeout. A large multipart file upload streams straight through
// (no body-size cap here — the upstream enforces its own), so this handler must
// be mounted outside the shared request-body limit.
//
//nolint:dupl // deliberately parallel to ResponsesIDs — both are single configured-target forwarders; a shared helper would hide their different config source (BatchTarget vs ResponsesTarget) and provider seam (BatchProvider vs ProxiableProvider)
func BatchHandler(src BatchSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if unsafeProxyPath(r.URL) {
			apierror.WriteOpenAI(w, http.StatusBadRequest,
				"pass-through path contains a disallowed traversal segment",
				"invalid_request_error", "invalid_proxy_path")
			return
		}

		name := src.BatchTarget()
		if name == "" {
			apierror.WriteOpenAI(w, http.StatusNotImplemented,
				"the batch and files endpoints are not configured; set batch_target",
				"invalid_request_error", "batch_not_configured")
			return
		}
		p, ok := src.Get(name)
		if !ok {
			// batch_target names a configured target whose provider is not
			// registered — its credential is absent — so nothing can serve it.
			apierror.WriteOpenAI(w, http.StatusNotImplemented,
				"the configured batch target is unavailable",
				"invalid_request_error", "batch_not_configured")
			return
		}
		bp, ok := providers.As[providers.BatchProvider](p)
		if !ok {
			apierror.WriteOpenAI(w, http.StatusNotImplemented,
				"provider "+p.Name()+" does not support the batch and files endpoints",
				"invalid_request_error", "batch_not_supported")
			return
		}

		target, err := url.Parse(bp.BatchBaseURL())
		if err != nil {
			// providerName comes from the configured registry, not user input.
			logger.Default().Error("invalid batch base URL", "provider", p.Name(), "error", err)
			apierror.WriteOpenAI(w, http.StatusInternalServerError, "upstream provider is unavailable", "server_error", "internal_error")
			return
		}

		// Files/Batches are a straight forward: no governance to score, so the
		// upstream status/error the forwarder reports are unused. Trace policy is
		// read per request (see passThroughHandler) so a live config reload of
		// propagate_passthrough takes effect on the next call.
		_, _ = forwardFixedTarget(w, r, target, bp.BatchAuthHeaders(), p.Name(), propagatesTrace(src), nil)
	}
}
