// Package proxy provides a transparent pass-through HTTP reverse proxy
// that forwards unhandled /v1/* requests to the matching upstream provider.
package proxy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/apierror"
	"github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/internal/streamio"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
	"go.opentelemetry.io/otel/propagation"
)

// gatewayIdentityHeaders address the gateway, not the provider. ReverseProxy
// clones every inbound header into the outbound request before Rewrite runs,
// so left alone these travel upstream verbatim, carrying whatever a caller
// put in them — including baggage-encoded user/session ids, which is exactly
// the identity leak trace-context injection is meant not to introduce.
var gatewayIdentityHeaders = []string{"baggage", "X-User-ID", "X-Session-ID"}

// stripGatewayIdentityHeaders deletes the caller-identity headers from the
// outbound request. See gatewayIdentityHeaders for why they must not travel
// upstream.
func stripGatewayIdentityHeaders(pr *httputil.ProxyRequest) {
	for _, h := range gatewayIdentityHeaders {
		pr.Out.Header.Del(h)
	}
}

// buildRewrite returns the ReverseProxy.Rewrite func for one resolved
// pass-through: point the request at target, drop headers that address the
// gateway rather than the provider, install the provider's own credential,
// and inject trace context when propagateTrace is set.
func buildRewrite(target *url.URL, authHeaders map[string]string, propagateTrace bool) func(*httputil.ProxyRequest) {
	return func(pr *httputil.ProxyRequest) {
		// Path and RawPath are trimmed of the same literal so the two
		// stay consistent for SetURL's escaping-aware join; /v1 contains
		// nothing that escapes, so both carry it verbatim.
		pr.Out.URL.Path = strings.TrimPrefix(pr.Out.URL.Path, "/v1")
		if pr.Out.URL.RawPath != "" {
			pr.Out.URL.RawPath = strings.TrimPrefix(pr.Out.URL.RawPath, "/v1")
		}
		pr.SetURL(target)
		pr.Out.Header.Del("X-Provider")
		pr.Out.Header.Del("Authorization")
		stripGatewayIdentityHeaders(pr)
		for k, v := range authHeaders {
			pr.Out.Header.Set(k, v)
		}
		pr.SetXForwarded()
		injectTraceContext(propagateTrace, pr)
	}
}

// proxyFlushInterval forces the reverse proxy to flush buffered bytes to the
// client immediately after each write. A negative value disables write
// buffering, which is required for incremental delivery of streamed
// pass-through endpoints (e.g. /v1/responses, /v1/audio/*, /v1/realtime).
const proxyFlushInterval = -1 * time.Nanosecond

// sanitizeScanBudget bounds the whole credential scan of a non-2xx response
// body, which is otherwise bounded only by maxRedactableBody — a size, not a
// time.
//
// The stream idle bound cannot stand in for one, in two independent ways. It is
// armed only after the scan returns; and an upstream that TRICKLES returns from
// every read promptly, so an idle timer is re-armed each time and never fires at
// all. The read then continues one keepalive at a time until 256 KiB have
// arrived, which at any realistic trickle rate is indefinitely. A provider
// answering 429 on a streaming endpoint while holding the connection open is
// exactly that shape, so this needs no hostile upstream to reach.
//
// Cancelling the upstream context unblocks the parked read, the same mechanism
// the idle bound uses. The budget is deliberately generous: it bounds a
// diagnostic body from an upstream that has already answered, where a healthy
// one delivers the whole thing in a read or two.
var sanitizeScanBudget = 15 * time.Second

// SetSanitizeScanBudgetForTest overrides the scan budget and returns a restore
// function, so a test can prove the bound without waiting out the real one.
func SetSanitizeScanBudgetForTest(d time.Duration) func() {
	prev := sanitizeScanBudget
	sanitizeScanBudget = d
	return func() { sanitizeScanBudget = prev }
}

// maxProxyPathDecodePasses covers ordinary, encoded, and repeatedly encoded
// path separators without allowing an attacker to turn path validation into an
// unbounded decode loop. A path still changing after this many passes is not a
// useful OpenAI resource path and is refused rather than interpreted differently
// by successive proxy layers.
const maxProxyPathDecodePasses = 8

// Handler returns an http.HandlerFunc that transparently forwards
// any /v1/* request to the matching upstream provider.
//
// This enables pass-through for endpoints the gateway does not handle
// natively (e.g. /v1/files, /v1/batches, /v1/fine_tuning, /v1/responses,
// /v1/audio/*, /v1/images/edits, /v1/realtime, etc.) while still injecting
// the correct provider authentication headers.
//
// Provider resolution order:
//  1. X-Provider request header (e.g. "X-Provider: openai")
//  2. "model" field in the JSON request body, resolved through src
//
// src is the authority on which provider owns a model name. Pass the *Gateway:
// it answers from the routing index — the models this instance's config named,
// the catalog and live discovery — so the pass-through agrees with the chat,
// completions, embeddings and image surfaces about who serves a model. A bare
// *providers.Registry also satisfies the interface but has neither catalog nor
// discovery, and answers from each provider's advisory SupportsModel; see
// providers.Registry.FindByModel for why that is not an ownership answer.
//
// A body that names no model at all is a 400 asking for X-Provider: this is the
// pass-through, and most of the endpoints it exists for (/v1/files,
// /v1/batches, /v1/models/{id}) carry no model to route on. A body that names a
// model no target owns is a 404 model_not_found — the same answer the natively
// handled surfaces give — rather than an opaque forward to whichever provider
// happened to be registered first.
func Handler(src providers.ProviderSource) http.HandlerFunc {
	next := passThroughHandler(src)
	return func(w http.ResponseWriter, r *http.Request) {
		// Validate before provider resolution reads the body and, critically,
		// before ReverseProxy.Rewrite installs the operator's provider credential.
		// Go deliberately preserves dot segments in URL.Path. Forwarding them to
		// an upstream that resolves them could escape the configured API root.
		if unsafeProxyPath(r.URL) {
			apierror.WriteOpenAI(w, http.StatusBadRequest,
				"pass-through path contains a disallowed traversal segment",
				"invalid_request_error",
				"invalid_proxy_path",
			)
			return
		}
		next(w, r)
	}
}

func passThroughHandler(src providers.ProviderSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Read per request, not once at Handler construction, so a live
		// ReloadConfig flip of observability.tracing.propagate_passthrough
		// takes effect on the next request rather than needing a restart.
		propagateTrace := propagatesTrace(src)
		p, model, ok := ResolveProvider(r, src)
		if !ok {
			// The caller named a provider explicitly. Telling them to set the
			// header they just set answers a question they did not ask. Whether
			// the name is unknown to this build or merely absent from the
			// config is deliberately one answer: the pass-through can be
			// unauthenticated, and the difference describes the deployment.
			if named := r.Header.Get("X-Provider"); named != "" {
				apierror.WriteOpenAI(w, http.StatusNotFound,
					"no configured target serves the requested provider",
					"invalid_request_error",
					"provider_not_found",
				)
				return
			}
			if model != "" {
				// The caller named a model and no configured target serves it.
				// Forwarding anyway is what sent the request body — prompt
				// included — to a provider that does not own the name, decided
				// by registration order rather than by anything the gateway
				// knows. The answer is written by apierror rather than composed
				// here, so this surface and the four routed ones report one
				// condition one way.
				apierror.WriteModelNotFound(w)
				return
			}
			apierror.WriteOpenAI(w, http.StatusBadRequest,
				`no provider resolved; set the X-Provider header (e.g. "X-Provider: openai") or include a "model" field in the request body`,
				"invalid_request_error",
				"provider_not_resolved",
			)
			return
		}

		pp, canProxy := providers.As[providers.ProxiableProvider](p)
		if !canProxy {
			apierror.WriteOpenAI(w, http.StatusNotImplemented,
				"provider "+p.Name()+" does not support proxy pass-through",
				"invalid_request_error",
				"proxy_not_supported",
			)
			return
		}

		// Non-OpenAI-wire providers (Anthropic, Gemini, Bedrock, Cohere, Vertex,
		// Azure) cannot serve a transparently-forwarded OpenAI-shaped request at
		// their base URL. Refuse with 501 instead of forwarding a request their
		// upstream cannot parse; they remain available via their native
		// translated endpoints. See core.NonOpenAIWireProvider.
		if _, nativeOnly := providers.As[providers.NonOpenAIWireProvider](p); nativeOnly {
			apierror.WriteOpenAI(w, http.StatusNotImplemented,
				"provider "+p.Name()+" is not available for OpenAI-compatible pass-through; use its native chat, embeddings, or images endpoints",
				"invalid_request_error",
				"proxy_not_supported",
			)
			return
		}

		providerName := p.Name()

		target, err := url.Parse(pp.BaseURL())
		if err != nil {
			// providerName comes from the configured registry, not raw user input.
			logger.Default().Error("invalid provider base URL", "provider", providerName, "error", err)
			apierror.WriteOpenAI(w, http.StatusInternalServerError, "upstream provider is unavailable", "server_error", "internal_error")
			return
		}

		// The proxy is mounted at /v1/*, so every inbound path already carries
		// the OpenAI /v1 prefix, and a provider's base URL is its API ROOT — the
		// one rule every surface of this repo applies to it, used verbatim. So
		// the operation hangs directly beneath the root: strip the gateway's own
		// /v1 from the INBOUND path and leave the configured root alone.
		//
		// Reading it the other way round — trimming a trailing /v1 off the root
		// and joining the whole inbound path — only works for a root whose
		// version segment happens to be last. It inserted the gateway's /v1
		// mid-path for every other shape: deepinfra's /v1/openai reached
		// /v1/openai/v1/responses, databricks' /serving-endpoints reached
		// /serving-endpoints/v1/responses, and an operator root mounted at
		// /custom/api reached /custom/api/v1/responses. All 404 upstream.
		//
		// A root carrying no path is no exception to that: it is still the API
		// root, and the operation still hangs directly beneath it. Perplexity's
		// is the bare host, so /v1/responses reaches /responses — which is where
		// its own chat surface builds from the same base. A provider whose
		// configured value is a SERVER root rather than an API root resolves the
		// difference itself, in New, and reports the API root here; that is what
		// ProxiableProvider.BaseURL asks for, and Ollama is the in-tree case.
		//
		// Branching on the path instead — preserving the gateway's /v1 for a
		// pathless root — read one operator value two ways with nothing to tell
		// them apart, since both shapes are a scheme and a host.

		authHeaders := pp.AuthHeaders()
		// The credential values this request will carry upstream, so a response
		// echoing one can be scanned for it before it reaches the client.
		secrets := injectedSecrets(authHeaders)

		// Use the raw SSE-tuned transport (no ResponseHeaderTimeout) so slow or
		// streaming pass-through endpoints are not cut off while waiting for the
		// upstream's first response header. The raw transport, not the
		// otelhttp-wrapped client, so no extra OTel CLIENT span is emitted per
		// proxied call; trace context is injected explicitly in Rewrite below,
		// governed by observability.tracing.propagate_passthrough.
		//
		// Providers requiring per-request signing (e.g. AWS SigV4) wrap that
		// transport so the fully-formed outbound request is signed; a signing
		// failure surfaces via ErrorHandler rather than as an unsigned forward.
		var transport http.RoundTripper = httpclient.SharedStreamingTransport()
		if signer, ok := providers.As[providers.RequestSigner](p); ok {
			transport = signingRoundTripper{base: transport, signer: signer}
		}

		// WrapResponseWriter clears http.Server's WriteTimeout after the first
		// write so long streams are not truncated. Cancelling this context on an
		// idle upstream is what replaces the bound that removal gives up.
		upstreamCtx, cancelUpstream := context.WithCancel(r.Context())
		defer cancelUpstream()
		r = r.WithContext(upstreamCtx)

		// The outcome of the forward, as the gateway lifecycle needs to hear it.
		// Both are written only from inside ServeHTTP, which returns before they
		// are read, so neither needs synchronising.
		var (
			forwardErr     error
			upstreamStatus int
		)

		proxy := &httputil.ReverseProxy{
			Transport:     transport,
			FlushInterval: proxyFlushInterval,
			Rewrite:       buildRewrite(target, authHeaders, propagateTrace),
			ModifyResponse: func(resp *http.Response) error {
				// Recorded, not acted on: returning an error here would make the
				// reverse proxy swallow the upstream's own response and answer
				// its ErrorHandler instead, so a caller would stop seeing the
				// 500 the upstream actually sent. The status is reported to the
				// lifecycle after the fact so the circuit breaker can score it.
				upstreamStatus = resp.StatusCode
				resp.Header.Set("X-Gateway-Provider", providerName)
				// Redact before wrapping: an upstream that quotes the credential
				// this proxy injected must not have that quote relayed to the
				// client, who never sent it. Headers are scanned on every
				// response, bodies on every non-2xx; success bodies pass through
				// untouched. See sanitizeResponse.
				//
				// The scan buffers that body under a size cap that is not a time
				// cap, and the idle bound installed below cannot stand in for one.
				// See sanitizeScanBudget.
				scanTimer := time.AfterFunc(sanitizeScanBudget, cancelUpstream)
				sanitizeErr := sanitizeResponse(resp, secrets)
				scanTimer.Stop()
				if sanitizeErr != nil {
					return sanitizeErr
				}
				// A 101 hands resp.Body to handleUpgradeResponse, which requires an
				// io.ReadWriteCloser, and a tunnelled connection (e.g. /v1/realtime)
				// is legitimately idle. Bound only ordinary response bodies.
				if resp.StatusCode != http.StatusSwitchingProtocols {
					resp.Body = streamio.NewIdleReadCloser(resp.Body, streamio.IdleTimeout(), cancelUpstream)
				}
				return nil
			},
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
				// The client's answer is written here, as it always was; the
				// error is also kept so the lifecycle records a failure and the
				// breaker scores one. Everything this handler answers is a
				// failure to reach or read the upstream.
				forwardErr = err
				var maxBytesErr *http.MaxBytesError
				if errors.As(err, &maxBytesErr) {
					apierror.WriteOpenAI(w, http.StatusRequestEntityTooLarge, "request body too large", "invalid_request_error", "request_too_large")
					return
				}
				// providerName comes from the configured registry, not raw user input.
				logger.Default().Error("proxy upstream error", "provider", providerName, "error", err)
				apierror.WriteOpenAI(w, http.StatusBadGateway,
					"upstream connection failed",
					"server_error",
					"upstream_error",
				)
			},
		}

		gov, governed := src.(passthroughGovernor)
		if !governed {
			// A source with no lifecycle to run — a bare *providers.Registry —
			// holds no plugins, no breakers and no limiters, so there is nothing
			// here to apply and nothing being skipped.
			proxy.ServeHTTP(streamio.WrapResponseWriter(w), r)
			return
		}

		// Projected before anything is forwarded, because a guardrail that
		// cannot read this body has to be able to refuse it while refusing is
		// still possible.
		body, inspectable := projectBody(r)

		// forwarded records whether the lifecycle ever got as far as calling the
		// upstream. It is what decides who answers the client: once ServeHTTP
		// has run, the response — success or ErrorHandler's — is already on the
		// wire and must not be written over. Before that, nothing has been
		// written and the refusal is ours to report.
		forwarded := false
		err = gov.RoutePassthrough(r.Context(), providerName, model, body, inspectable,
			func(ctx context.Context) error {
				forwarded = true
				proxy.ServeHTTP(streamio.WrapResponseWriter(w), r.WithContext(ctx))
				if forwardErr != nil {
					return forwardErr
				}
				// A 5xx is the upstream failing, and the breaker exists to stop
				// calling a target that is failing. 4xx is deliberately not one:
				// a rejected request is the caller's fault, and tripping a
				// shared breaker on it would let one client's bad requests take
				// a healthy provider out for everybody.
				//
				// The error never reaches the client — the upstream's own
				// response is already on the wire — so it carries no upstream
				// text. It exists to be counted, logged and scored.
				if upstreamStatus >= http.StatusInternalServerError {
					return core.StatusError(providerName, upstreamStatus, "")
				}
				return nil
			})
		if err != nil && !forwarded {
			apierror.WriteRouteError(w, err)
		}
	}
}

// unsafeProxyPath reports whether any decoding layer can turn the caller's
// path into a dot-segment traversal. PathUnescape is deliberately repeated:
// gateways and provider control planes do not always decode the same number of
// times, so accepting a double-encoded parent segment here would recreate the
// root escape at the next layer. Backslashes are treated as separators because
// some upstream frameworks normalize them even though RFC 3986 does not.
func unsafeProxyPath(u *url.URL) bool {
	path := u.EscapedPath()
	for range maxProxyPathDecodePasses {
		if hasDotSegment(path) {
			return true
		}
		if !hasEncodedOctet(path) {
			return false
		}

		decoded, err := url.PathUnescape(path)
		if err != nil {
			return true
		}
		if decoded == path {
			return false
		}
		path = decoded
	}

	// More layers than the gateway is willing to interpret are ambiguous by
	// construction. Refuse them rather than let a downstream choose a meaning.
	return true
}

func hasDotSegment(path string) bool {
	path = strings.ReplaceAll(path, `\`, "/")
	for segment := range strings.SplitSeq(path, "/") {
		// Several upstream routers discard semicolon-delimited matrix
		// parameters before cleaning a path. Judge the resource-name half so
		// "..;revision=1" cannot become ".." only after credential injection.
		name, _, _ := strings.Cut(segment, ";")
		if name == "." || name == ".." {
			return true
		}
	}
	return false
}

func hasEncodedOctet(path string) bool {
	for i := 0; i+2 < len(path); i++ {
		if path[i] == '%' && isHex(path[i+1]) && isHex(path[i+2]) {
			return true
		}
	}
	return false
}

func isHex(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F'
}

// passthroughGovernor is a ProviderSource that can also run the gateway request
// lifecycle around a forward: plugin stages, circuit breaker, per-target
// concurrency, request logging and cost accounting. *Gateway implements it.
//
// Declared here rather than widened into providers.ProviderSource for the same
// reason targetGatedSource is: this is the only caller that needs the answer,
// and a Registry cannot answer it — it holds no plugins, breakers or limiters
// to run anything with.
//
// Retry is deliberately absent from what it applies. See
// Gateway.RoutePassthrough.
type passthroughGovernor interface {
	RoutePassthrough(ctx context.Context, target, model, body string, bodyInspectable bool, forward func(context.Context) error) error
}

// passthroughTracePolicy is a source that says whether forwards carry the
// gateway's trace context upstream. *Gateway implements it from
// observability.tracing.propagate_passthrough. A source that does not — a bare
// *providers.Registry — propagates, which is the configured default.
type passthroughTracePolicy interface {
	PropagatesPassthroughTrace() bool
}

// propagatesTrace takes any of this package's source interfaces (the generic
// pass-through's providers.ProviderSource, or the narrower BatchSource /
// ResponsesSource) — all it needs is the optional passthroughTracePolicy
// type assertion, so the parameter stays untyped rather than widening every
// caller's source interface to carry methods it does not otherwise need.
func propagatesTrace(src any) bool {
	policy, ok := src.(passthroughTracePolicy)
	return !ok || policy.PropagatesPassthroughTrace()
}

// injectTraceContext writes the outbound request's W3C trace context header
// when propagation is enabled. Trace context only — the composite propagator
// would also forward baggage, and baggage carries the caller's user and
// session ids, which a provider has no need of.
func injectTraceContext(propagate bool, pr *httputil.ProxyRequest) {
	if !propagate {
		return
	}
	propagation.TraceContext{}.Inject(pr.Out.Context(), propagation.HeaderCarrier(pr.Out.Header))
}

// signingRoundTripper signs each outbound proxied request via a provider's
// RequestSigner before delegating to the base transport. A signing failure is
// returned to the reverse proxy (surfaced as a 502 by ErrorHandler) rather than
// forwarding an unsigned request upstream.
type signingRoundTripper struct {
	base   http.RoundTripper
	signer providers.RequestSigner
}

func (s signingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := s.signer.SignProxyRequest(req); err != nil {
		return nil, fmt.Errorf("sign proxy request: %w", err)
	}
	return s.base.RoundTrip(req)
}

// targetGatedSource is a ProviderSource that also knows which providers the
// configured targets name. *Gateway implements it; a bare *providers.Registry
// does not, and cannot — it holds no config and therefore has no allowlist to
// apply, so a source without this method gates nothing.
//
// Declared here rather than widened into providers.ProviderSource because this
// is the only caller that needs the answer, and because a Registry answering
// the question at all would have to claim every registered provider is
// targeted — reintroducing exactly the conflation this exists to prevent.
type targetGatedSource interface {
	IsTargetedProvider(name string) bool
}

// providerIsAllowed reports whether the pass-through may send a body to this
// provider: some configured target must name it.
//
// Without this, the pass-through reached any provider whose credential happened
// to be in the environment, chosen by model ownership or by a client-supplied
// header rather than by configuration. An operator running `targets: [groq]`
// had requests — prompt included — forwarded to openai under the gateway's own
// openai credential. The four routed surfaces have always gated on targets;
// this is the surface that did not.
func providerIsAllowed(src providers.ProviderSource, name string) bool {
	gated, ok := src.(targetGatedSource)
	if !ok {
		return true
	}
	return gated.IsTargetedProvider(name)
}

// ResolveProvider determines which provider should receive the request. It
// checks the X-Provider header first, then falls back to model-based lookup by
// peeking at (and restoring) the JSON request body.
//
// The returned model is the name the body carried, reported whether or not it
// resolved, so the caller can tell "you named no model" from "nothing serves the
// model you named" — two different answers to the client.
//
// X-Provider is NOT gated on model ownership, deliberately. Ownership gating
// exists to stop the gateway PICKING a provider for a name it cannot place; a
// header is the caller placing it themselves. It is also the only way to reach
// the endpoints this proxy exists for whose model ids no index can enumerate — a
// fine-tune job's base model, a /v1/responses model newer than the catalog — so
// gating it would leave those unreachable with no escape hatch. The header is
// therefore both the explicit choice and the documented override for a model
// the index does not own.
//
// It IS gated on target membership, which is a different question and was the
// hole: the header's only bound used to be "a provider this instance has
// credentials for", and a credential present in the environment is not a
// provider the operator configured. Requiring a target keeps the escape hatch
// whole — an unenumerable model still resolves — while refusing to send a body
// somewhere the config never named.
func ResolveProvider(r *http.Request, src providers.ProviderSource) (providers.Provider, string, bool) {
	// 1. Explicit header takes precedence.
	if name := r.Header.Get("X-Provider"); name != "" {
		p, ok := src.Get(name)
		if ok && !providerIsAllowed(src, p.Name()) {
			return nil, "", false
		}
		return p, "", ok
	}

	// 2. Try to extract "model" from the request body.
	if r.Body == nil || r.ContentLength == 0 {
		return nil, "", false
	}

	model, err := ExtractTopLevelModel(r)
	if err != nil || model == "" {
		return nil, "", false
	}
	p, ok := src.FindByModel(model)
	// Ownership answers who serves the name; it does not answer whether this
	// gateway is configured to send anything there.
	if ok && !providerIsAllowed(src, p.Name()) {
		return nil, model, false
	}
	return p, model, ok
}

// ExtractTopLevelModel peeks at the JSON body to find the top-level "model"
// field, then restores the body so it can be read again by downstream handlers.
func ExtractTopLevelModel(r *http.Request) (string, error) {
	if r.Body == nil {
		return "", io.EOF
	}

	scanner := newTopLevelModelScanner(r.Body)
	model, err := scanner.extract()
	r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(scanner.captured.Bytes()), r.Body))
	if err != nil {
		return "", err
	}
	return model, nil
}

// topLevelModelScanner is a low-allocation JSON scanner that reads only the
// top-level "model" key from a JSON object without decoding the full document.
type topLevelModelScanner struct {
	reader   *bufio.Reader
	captured bytes.Buffer
}

func newTopLevelModelScanner(r io.Reader) *topLevelModelScanner {
	s := &topLevelModelScanner{}
	s.reader = bufio.NewReaderSize(io.TeeReader(r, &s.captured), 4096)
	return s
}

func (s *topLevelModelScanner) extract() (string, error) {
	tok, err := s.nextNonSpaceByte()
	if err != nil {
		return "", err
	}
	if tok != '{' {
		return "", nil
	}

	for {
		tok, err = s.nextNonSpaceByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return "", nil
			}
			return "", err
		}
		if tok == '}' {
			return "", nil
		}
		if tok != '"' {
			return "", nil
		}

		key, err := s.readJSONString()
		if err != nil {
			return "", err
		}
		tok, err = s.nextNonSpaceByte()
		if err != nil {
			return "", err
		}
		if tok != ':' {
			return "", nil
		}

		if key == "model" {
			tok, err := s.nextNonSpaceByte()
			if err != nil {
				return "", err
			}
			if tok != '"' {
				if err := s.skipJSONValue(tok); err != nil {
					return "", err
				}
				return "", nil
			}
			return s.readJSONString()
		}

		tok, err = s.nextNonSpaceByte()
		if err != nil {
			return "", err
		}
		if err := s.skipJSONValue(tok); err != nil {
			return "", err
		}

		tok, err = s.nextNonSpaceByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return "", nil
			}
			return "", err
		}
		switch tok {
		case ',':
			continue
		case '}':
			return "", nil
		default:
			return "", nil
		}
	}
}

func (s *topLevelModelScanner) nextNonSpaceByte() (byte, error) {
	for {
		b, err := s.reader.ReadByte()
		if err != nil {
			return 0, err
		}
		switch b {
		case ' ', '\n', '\r', '\t':
			continue
		default:
			return b, nil
		}
	}
}

func (s *topLevelModelScanner) readJSONString() (string, error) {
	buf := make([]byte, 0, 32)
	escaped := false
	for {
		b, err := s.reader.ReadByte()
		if err != nil {
			return "", err
		}
		if escaped {
			buf = append(buf, '\\', b)
			escaped = false
			continue
		}
		switch b {
		case '\\':
			escaped = true
		case '"':
			if bytes.IndexByte(buf, '\\') == -1 {
				return string(buf), nil
			}
			return strconv.Unquote(`"` + string(buf) + `"`)
		default:
			buf = append(buf, b)
		}
	}
}

func (s *topLevelModelScanner) skipJSONValue(first byte) error {
	switch first {
	case '"':
		_, err := s.readJSONString()
		return err
	case '{', '[':
		return s.skipComposite(first)
	default:
		return s.skipScalar()
	}
}

func (s *topLevelModelScanner) skipComposite(open byte) error {
	var closeCh byte
	switch open {
	case '{':
		closeCh = '}'
	case '[':
		closeCh = ']'
	default:
		return nil
	}

	depth := 1
	for depth > 0 {
		b, err := s.reader.ReadByte()
		if err != nil {
			return err
		}
		switch b {
		case '"':
			if _, err := s.readJSONString(); err != nil {
				return err
			}
		case open:
			depth++
		case closeCh:
			depth--
		case '{':
			if open != '{' {
				if err := s.skipComposite(b); err != nil {
					return err
				}
			}
		case '[':
			if open != '[' {
				if err := s.skipComposite(b); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *topLevelModelScanner) skipScalar() error {
	for {
		b, err := s.reader.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		switch b {
		case ',', '}', ']', ' ', '\n', '\r', '\t':
			if err := s.reader.UnreadByte(); err != nil {
				return err
			}
			return nil
		}
	}
}
