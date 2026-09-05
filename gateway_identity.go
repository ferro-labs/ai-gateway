package aigateway

import (
	"context"

	"github.com/ferro-labs/ai-gateway/internal/otel"
	"github.com/ferro-labs/ai-gateway/observability"
)

// requestIdentity resolves the identity a request is recorded under. The
// context's identity — set by the HTTP layer from X-User-ID, X-Session-ID and
// baggage, or by an embedder — is the base; a non-empty body `user` replaces
// the user id, because it is the caller's most explicit statement and the one
// field the OpenAI request format defines for the purpose. The body value is
// run through the same rule the HTTP headers are: a value that cannot be an
// id — empty after trimming, longer than the header path allows, or carrying
// a control character — is simply not applied, exactly as an unusable header
// is left out, rather than failing the request. Surfaces whose body carries
// no `user` pass "".
//
// ctx is returned unchanged when nothing changes, so an anonymous request
// allocates nothing here.
func requestIdentity(ctx context.Context, bodyUser string) (context.Context, observability.RequestIdentity) {
	id := observability.RequestIdentityFromContext(ctx)
	bodyUser = otel.IdentityValue(bodyUser)
	if bodyUser == "" || bodyUser == id.User {
		return ctx, id
	}
	id.User = bodyUser
	return observability.ContextWithRequestIdentity(ctx, id), id
}
