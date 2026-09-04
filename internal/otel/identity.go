package otel

import (
	"context"
	"net/http"
	"strings"

	"github.com/ferro-labs/ai-gateway/observability"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
)

// Request identity inputs the HTTP layer reads. Headers outrank baggage
// because a header is set for this hop on purpose, while baggage is whatever
// an upstream service happened to propagate.
const (
	userIDHeader      = "X-User-ID"
	sessionIDHeader   = "X-Session-ID"
	baggageHeader     = "baggage"
	baggageUserKey    = "user.id"
	baggageSessionKey = "session.id"

	// maxIdentityValueLen bounds one id. An id is an opaque token, not a
	// document; a longer value is dropped rather than truncated, because a
	// truncated id names a different user than the caller did.
	maxIdentityValueLen = 256
)

// requestIdentityFromHeaders reads the end-user and session ids a request
// carries. Baggage is parsed directly with the W3C baggage propagator rather
// than through the global propagator, so it is read identically whether or not
// tracing is enabled — the identity feeds the request log too.
func requestIdentityFromHeaders(ctx context.Context, h http.Header) observability.RequestIdentity {
	id := observability.RequestIdentity{
		User:      identityValue(h.Get(userIDHeader)),
		SessionID: identityValue(h.Get(sessionIDHeader)),
	}
	if (id.User != "" && id.SessionID != "") || h.Get(baggageHeader) == "" {
		return id
	}
	bag := baggage.FromContext(propagation.Baggage{}.Extract(ctx, propagation.HeaderCarrier(h)))
	if id.User == "" {
		id.User = identityValue(bag.Member(baggageUserKey).Value())
	}
	if id.SessionID == "" {
		id.SessionID = identityValue(bag.Member(baggageSessionKey).Value())
	}
	return id
}

// identityValue returns v trimmed, or "" when v cannot be an id: empty, longer
// than maxIdentityValueLen, or carrying a control character.
func identityValue(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || len(v) > maxIdentityValueLen {
		return ""
	}
	for i := 0; i < len(v); i++ {
		if v[i] < 0x20 || v[i] == 0x7f {
			return ""
		}
	}
	return v
}
