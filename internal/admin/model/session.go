package model

import (
	"time"
)

// SessionTokenPrefix distinguishes a dashboard session token from a stored API
// key at a glance, so AuthMiddleware can route a bearer to the right store
// without probing both. It deliberately does not begin with the api-key prefix
// "fgw_", so neither value can be mistaken for the other.
const SessionTokenPrefix = "fgws_"

// Session is one authenticated dashboard sign-in. It is deliberately not an
// APIKey: a key is a machine credential an operator manages, a session is a
// short-lived artifact of a human signing in, and conflating the two is the
// defect this type exists to remove.
//
// The token itself is never stored — only its hash — and is returned exactly
// once, from CreateSession.
type Session struct {
	ID string `json:"id"`
	// CredentialID is the ID of the APIKey this session was minted from — the
	// stored key's ID, or the synthetic master-key ID NewCredentialValidator
	// fabricates. It exists so a session's liveness can be re-checked against
	// its source credential (see AuthMiddlewareWithSessions) and so
	// GET /admin/sessions can say which key a session came from, which Subject
	// alone cannot: it is an operator-chosen name and is not unique.
	CredentialID string     `json:"credential_id"`
	Subject      string     `json:"subject"`
	Scopes       []string   `json:"scopes"`
	CreatedAt    time.Time  `json:"created_at"`
	LastSeenAt   *time.Time `json:"last_seen_at,omitempty"`
	ExpiresAt    time.Time  `json:"expires_at"`
}
