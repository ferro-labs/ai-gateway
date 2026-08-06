package otel

import (
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/ferro-labs/ai-gateway/pkg/logger"
)

// The standard OTLP endpoint variables. They are read here only to decide
// whether the SDK is already configured; their *values* are never interpreted
// by the gateway — see resolveExportTarget.
const (
	envEndpoint       = "OTEL_EXPORTER_OTLP_ENDPOINT"
	envTracesEndpoint = "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"
)

// tracesSignalPath is the path the OTLP/HTTP traces signal is sent to, relative
// to a base endpoint. Fixed by the specification, not configurable.
const tracesSignalPath = "v1/traces"

// exportTarget is how the span exporter is pointed at a collector.
//
// The zero value means "pass no endpoint option at all", which hands
// OTEL_EXPORTER_OTLP_ENDPOINT and OTEL_EXPORTER_OTLP_TRACES_ENDPOINT to the
// SDK's own handling of them. That is deliberate: those two variables have
// precise and *different* semantics in the specification — the first is a base
// URL that v1/traces is appended to, the second is used verbatim — and the SDK
// already implements both correctly. Re-deriving them here is how the gateway
// previously turned a collector URL into a percent-encoded hostname and
// exported nothing.
type exportTarget struct {
	// url is a complete endpoint URL, scheme and path included, ready to hand
	// to WithEndpointURL.
	url string
	// hostPort is a bare host:port with no scheme, which has always meant
	// plaintext here and still does.
	hostPort string
}

// log reports the resolved endpoint spans are being sent to. It is the URL the
// exporter will actually POST to, signal path included, so an operator can
// compare it against their collector without deriving it.
func (t exportTarget) log() {
	switch {
	case t.url != "":
		logger.Default().Info("otel: exporting traces", "endpoint", t.url)
	case t.hostPort != "":
		logger.Default().Info("otel: exporting traces", "endpoint", t.hostPort, "transport_security", "plaintext")
	default:
		logger.Default().Info("otel: exporting traces to the endpoint named by the OTEL_EXPORTER_OTLP_* environment")
	}
}

// isHTTPProtocol reports whether the configured protocol selects OTLP/HTTP.
func isHTTPProtocol(protocol string) bool {
	return protocol == "http/protobuf" || protocol == "http"
}

// resolveExportTarget decides how to point the exporter at a collector,
// following the OTLP exporter specification
// (https://opentelemetry.io/docs/specs/otel/protocol/exporter/).
//
// Precedence is unchanged: the standard environment variables win over the
// configured endpoint. What changed is that the gateway no longer parses them.
// When either is set it returns the zero exportTarget, so no endpoint option is
// passed and the SDK applies the specification's rules itself — appending
// v1/traces to the non-signal-specific OTEL_EXPORTER_OTLP_ENDPOINT, using
// OTEL_EXPORTER_OTLP_TRACES_ENDPOINT verbatim, and honouring the scheme.
//
// observability.tracing.endpoint is the configured analogue of
// OTEL_EXPORTER_OTLP_ENDPOINT and is therefore treated the same way: a base
// endpoint, with the traces signal path appended for OTLP/HTTP. Giving it a
// third, gateway-specific meaning would leave operators unable to reason about
// their own collector's documentation. It accepts one thing the environment
// variable does not — a base that already ends in the signal path is used as
// written rather than having it appended twice; see parseConfiguredEndpoint.
func resolveExportTarget(cfg Config, getenv func(string) string, httpProtocol bool) (exportTarget, error) {
	if getenv(envEndpoint) != "" || getenv(envTracesEndpoint) != "" {
		return exportTarget{}, nil
	}
	return parseConfiguredEndpoint(cfg.Endpoint, httpProtocol)
}

// parseConfiguredEndpoint validates observability.tracing.endpoint and resolves
// it to an exportTarget. An empty endpoint is not an error — it means tracing is
// driven by the environment, or not at all.
//
// A value that cannot be understood is rejected rather than passed on. An
// endpoint the exporter cannot use is not a degraded configuration, it is a
// silently disabled one: the export fails per batch, deep inside the SDK, long
// after the operator has stopped watching the logs.
func parseConfiguredEndpoint(raw string, httpProtocol bool) (exportTarget, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return exportTarget{}, nil
	}

	// No scheme: a bare host:port, which has always meant plaintext here.
	// OTLP/gRPC endpoints are conventionally written this way and the
	// specification allows any form the gRPC client accepts, so this stays.
	if !strings.Contains(raw, "://") {
		if strings.ContainsAny(raw, "/?#") {
			return exportTarget{}, fmt.Errorf(
				"tracing endpoint %q has a path but no scheme: write it as a URL (http://host:port/...) or as a bare host:port", raw)
		}
		return exportTarget{hostPort: raw}, nil
	}

	u, err := url.Parse(raw)
	if err != nil {
		return exportTarget{}, fmt.Errorf("tracing endpoint %q is not a valid URL: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return exportTarget{}, fmt.Errorf("tracing endpoint %q has scheme %q: only http and https are supported", raw, u.Scheme)
	}
	if u.Host == "" {
		return exportTarget{}, fmt.Errorf("tracing endpoint %q has no host", raw)
	}

	// OTLP/gRPC defines no meaning for a path, so the URL is passed through
	// with only its scheme and host consulted.
	if !httpProtocol {
		return exportTarget{url: raw}, nil
	}

	// OTLP/HTTP: the base endpoint plus the traces signal path — unless the
	// operator already wrote it.
	//
	// Appending unconditionally is what the specification says to do with a base
	// endpoint, and it is also what every collector's own documentation defeats:
	// the URL those docs print is the signal URL, so pasting it produced
	// .../v1/traces/v1/traces, a 404 per export batch and not one span stored.
	// Nothing serves the traces signal at a path ending in the signal path
	// twice, so treating an endpoint that already ends in it as complete cannot
	// mistake a working collector for a mistyped one — while rejecting the value
	// outright would turn one pasted URL into a boot failure.
	if cleaned := path.Clean("/" + u.Path); strings.HasSuffix(cleaned, "/"+tracesSignalPath) {
		u.Path = cleaned
	} else {
		u.Path = path.Join(u.Path, tracesSignalPath)
	}
	return exportTarget{url: u.String()}, nil
}
