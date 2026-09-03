package aigateway

import (
	"context"
	"net/http"
	"strconv"
)

// RoutingAttribution names the target that served a routed request, or on
// failure the last one attempted. A caller that wants it hands an empty value
// to the context with WithRoutingAttribution before calling Route,
// RouteStream, Embed, GenerateImage, Rerank, Moderate, Transcribe or Speech;
// the request pipeline fills it when the walk ends — for a stream, before the
// first chunk is delivered — so HTTP handlers can emit it as response headers
// ahead of the body.
type RoutingAttribution struct {
	// Provider is the canonical provider of the serving target (e.g. openai).
	Provider string
	// Target is the target key as the operator wrote it: targets[].virtual_key.
	Target string
	// Model is the upstream model sent to the provider, after model_map.
	Model string
	// Attempts is the number of routing-layer attempts for this request:
	// physical provider calls and local circuit-breaker or concurrency
	// refusals, across every target tried.
	Attempts int
}

// Response header names carrying RoutingAttribution. They are emitted on every
// routed surface; a host that embeds the gateway can emit the same names from
// the value it reads back.
const (
	HeaderGatewayProvider = "X-Gateway-Provider"
	HeaderGatewayTarget   = "X-Gateway-Target"
	HeaderGatewayModel    = "X-Gateway-Model"
	HeaderGatewayAttempts = "X-Gateway-Attempts"
)

// SetHeaders writes the attribution headers into h. Nothing is written until a
// target was attempted, so a request refused before routing carries none.
func (a *RoutingAttribution) SetHeaders(h http.Header) {
	if a == nil || a.Target == "" {
		return
	}
	h.Set(HeaderGatewayProvider, a.Provider)
	h.Set(HeaderGatewayTarget, a.Target)
	h.Set(HeaderGatewayModel, a.Model)
	h.Set(HeaderGatewayAttempts, strconv.Itoa(a.Attempts))
}

type routingAttributionKey struct{}

// WithRoutingAttribution returns a context that asks the request pipeline to
// fill a with the routing outcome. a must outlive the call.
func WithRoutingAttribution(ctx context.Context, a *RoutingAttribution) context.Context {
	return context.WithValue(ctx, routingAttributionKey{}, a)
}

// recordAttribution fills the caller's RoutingAttribution, if one rides ctx.
func recordAttribution(ctx context.Context, target routedTarget, attempts int) {
	a, _ := ctx.Value(routingAttributionKey{}).(*RoutingAttribution)
	if a == nil || target.key == "" {
		return
	}
	*a = RoutingAttribution{Provider: target.priceProvider, Target: target.key, Model: target.upstreamModel, Attempts: attempts}
}
