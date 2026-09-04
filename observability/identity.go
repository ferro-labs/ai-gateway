package observability

import "context"

// RequestIdentity is who a request was made for, as the caller stated it: an
// end-user id, a session id grouping one conversation, and free-form request
// metadata. The gateway records it on the request span, on lifecycle and
// routing-attempt events and on request-log rows. It is never forwarded to a
// provider by this package. Every field is optional; the zero value means the
// caller supplied nothing.
type RequestIdentity struct {
	// User is the end-user id, recorded as the enduser.id span attribute. The
	// HTTP layer reads it from the OpenAI `user` field, the X-User-ID header
	// or the baggage entry user.id, in that order of precedence.
	User string
	// SessionID groups the requests of one conversation, recorded as
	// session.id. Read from X-Session-ID or the baggage entry session.id.
	SessionID string
	// Metadata is recorded as one ferro.request.metadata.<key> attribute per
	// entry. Only an embedder sets it; the HTTP layer reads no metadata. The
	// map is read after the request has been answered, so it must not be
	// mutated once handed to ContextWithRequestIdentity.
	Metadata map[string]string
}

// IsZero reports whether the caller supplied no identity at all.
func (id RequestIdentity) IsZero() bool {
	return id.User == "" && id.SessionID == "" && len(id.Metadata) == 0
}

type identityKey struct{}

// ContextWithRequestIdentity returns ctx carrying id. The HTTP layer calls it
// from request headers; an embedder calls it before Route, RouteStream or any
// other gateway entry point.
func ContextWithRequestIdentity(ctx context.Context, id RequestIdentity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// RequestIdentityFromContext returns the identity ctx carries, or the zero
// value when none was set. It allocates nothing.
func RequestIdentityFromContext(ctx context.Context) RequestIdentity {
	id, _ := ctx.Value(identityKey{}).(RequestIdentity)
	return id
}
