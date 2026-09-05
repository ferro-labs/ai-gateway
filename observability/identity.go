package observability

import (
	"context"
	"sort"
)

const (
	// maxMetadataEntries bounds the number of Metadata entries recorded. It
	// is generous enough for the handful of fields an embedder legitimately
	// attaches (a team, a tenant, a feature flag, ...) while keeping one
	// request from stamping enough span attributes to push a routing
	// attempt's span past a typical OTLP collector's per-span attribute
	// limit.
	maxMetadataEntries = 32
	// maxMetadataKeyLen and maxMetadataValueLen bound one entry, matching the
	// id scalars' rationale in internal/otel/identity.go: metadata is short,
	// structured context, not a document.
	maxMetadataKeyLen   = 128
	maxMetadataValueLen = 256
)

// RequestIdentity is who a request was made for, as the caller stated it: an
// end-user id, a session id grouping one conversation, and free-form request
// metadata. The gateway records it on the request span, on lifecycle and
// routing-attempt events and on request-log rows. It is never forwarded to a
// provider by this package. Every field is optional; the zero value means the
// caller supplied nothing.
type RequestIdentity struct {
	// User is the end-user id, recorded as the enduser.id span attribute. The
	// HTTP layer reads it from the X-User-ID header or the baggage entry
	// user.id; the gateway core then overlays the OpenAI body `user` field
	// on top, so a body value always outranks either header.
	User string
	// SessionID groups the requests of one conversation, recorded as
	// session.id. Read from X-Session-ID or the baggage entry session.id.
	SessionID string
	// Metadata is recorded as one ferro.request.metadata.<key> attribute per
	// entry. Only an embedder sets it; the HTTP layer reads no metadata.
	// ContextWithRequestIdentity bounds it to maxMetadataEntries entries of
	// at most maxMetadataKeyLen / maxMetadataValueLen each, dropping
	// whatever exceeds those limits deterministically rather than erroring;
	// the map is read after the request has been answered, so it must not be
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
//
// id.Metadata is bounded before it is stored — see RequestIdentity.Metadata —
// so every reader downstream (span attributes, routing-attempt and lifecycle
// events, request-log rows) sees an already-bounded map without bounding it
// again itself.
func ContextWithRequestIdentity(ctx context.Context, id RequestIdentity) context.Context {
	id.Metadata = boundedMetadata(id.Metadata)
	return context.WithValue(ctx, identityKey{}, id)
}

// boundedMetadata returns a copy of m capped at maxMetadataEntries entries,
// each within maxMetadataKeyLen / maxMetadataValueLen, or nil when m is
// empty. Entries are considered in sorted key order so that which entries
// survive the cap is deterministic — ranging m directly would let Go's
// randomised map iteration pick a different surviving set on every call for
// identical input.
func boundedMetadata(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make(map[string]string, min(len(m), maxMetadataEntries))
	for _, k := range keys {
		if len(out) == maxMetadataEntries {
			break
		}
		v := m[k]
		if len(k) > maxMetadataKeyLen || len(v) > maxMetadataValueLen {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// RequestIdentityFromContext returns the identity ctx carries, or the zero
// value when none was set. It allocates nothing.
func RequestIdentityFromContext(ctx context.Context) RequestIdentity {
	id, _ := ctx.Value(identityKey{}).(RequestIdentity)
	return id
}
