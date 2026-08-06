package model

import (
	"context"

	"github.com/ferro-labs/ai-gateway/internal/authctx"
)

// ActorUnrecorded is the stored value when a mutation had no authenticated
// caller in context — a change applied by the process itself at startup, or by
// a test harness.
//
// It is distinct from the empty string, which means something different and
// older: a row written before actors were recorded at all. Collapsing the two
// would let "we were not tracking this yet" be read as "nobody did it".
const ActorUnrecorded = "unattributed"

// ActorFromContext describes who is acting, as a frozen display string for an
// audit record.
//
// It is denormalised on purpose. A foreign key into api_keys would resolve to
// nothing once that key is deleted, and a deleted key is precisely when the
// question "who did this" gets asked. The name is therefore copied in at write
// time and never refreshed: the record states who the actor was called when
// they acted, not what that row says today.
//
// The returned string carries no secret. APIKey.ID is an opaque identifier
// derived from the stored row, never the bearer token.
func ActorFromContext(ctx context.Context) string {
	key, ok := APIKeyFromContext(ctx)
	if !ok {
		return ActorUnrecorded
	}

	// For a dashboard session, key.ID is the session's own identifier and the
	// authctx slot holds the credential the session was minted from. The
	// credential is the actor — the session is just how they were holding it —
	// so it is preferred whenever present.
	id, hasSource := authctx.KeyID(ctx)
	if !hasSource || id == "" {
		id = key.ID
	}
	return FormatActor(key.Name, id)
}

// FormatActor renders the frozen display string for an audit record from a
// name and an id, either of which may be empty.
func FormatActor(name, id string) string {
	switch {
	// The synthetic master-key credential uses the same string for both, and
	// "master-key (master-key)" reads as though two different things happened
	// to match.
	case id != "" && name == id:
		return id
	case name != "" && id != "":
		return name + " (" + id + ")"
	case name != "":
		return name
	case id != "":
		return id
	default:
		return ActorUnrecorded
	}
}
