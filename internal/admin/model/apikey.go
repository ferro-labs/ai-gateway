// Package model holds the admin control-plane domain types — API keys,
// sessions, audit entries, and persisted config versions — plus the api-key /
// actor context kernel shared across package boundaries. It is the foundation
// layer: both the repository (storage) and handlers (HTTP) packages import it,
// and it imports neither.
package model

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// ErrKeyNotFound is returned by Store implementations when an operation targets
// an API key ID that does not exist. Handlers use errors.Is to distinguish a
// genuine not-found (HTTP 404) from an internal or transient store failure
// (HTTP 500), so a database outage is never reported to callers as a 404.
var ErrKeyNotFound = errors.New("key not found")

// APIKey represents an API key for authenticating requests to the gateway.
//
// Key holds the display form (see displayKey) on every value a Store reads
// back. The full secret appears only on the values returned by Create and
// RotateKey, which build it in memory — it is never stored and cannot be
// recovered afterwards.
type APIKey struct {
	ID         string     `json:"id"`
	Key        string     `json:"key"`
	Name       string     `json:"name"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	RotatedAt  *time.Time `json:"rotated_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	UsageCount int64      `json:"usage_count"`
	Active     bool       `json:"active"`
}

// ScopeAdmin and ScopeReadOnly are the API key permission scopes. They live in
// this foundation package because HasAdminScope below, KeyStore.Create in
// repository, and the auth middleware and route guards in handlers all reference
// them, and none of those packages may import the others.
const (
	ScopeAdmin    = "admin"
	ScopeReadOnly = "read_only"
)

// validScopes is the closed set a key may carry, in the order an error message
// offers them. It sits beside the constants so a new scope cannot be added
// without the validator learning about it.
var validScopes = []string{ScopeAdmin, ScopeReadOnly}

// HasAdminScope reports whether scopes grant admin.
func HasAdminScope(scopes []string) bool {
	return slices.Contains(scopes, ScopeAdmin)
}

// ValidateScopes reports the first scope this gateway does not recognize, or
// nil when every entry is known. An empty list is valid: an unspecified list is
// not a misspelled one, and both callers already assign it a meaning — a new
// key falls back to the store's least-privilege default, an update leaves the
// existing scopes alone — so demanding the field would break every client that
// has never sent it without catching a single typo.
//
// Unknown scopes are refused rather than ignored. Ignoring them is the right
// answer where the party issuing a credential and the party enforcing it are
// separate systems on separate release trains: there, a scope the issuer does
// not recognize may still mean something to a resource server, so rejecting it
// breaks a newer client talking to an older issuer for no gain. That is not
// this system. The gateway mints these scopes and is the only thing that ever
// enforces them, both from the same binary, so a scope this code does not know
// grants exactly nothing and can never come to mean anything. Accepting one
// issues a credential that authenticates, lists as valid in GET /admin/keys,
// and is refused by every route it is presented to — a typo whose only symptom
// arrives in production. RFC 6749 §3.3 leaves the choice to the server and
// defines invalid_scope for precisely this refusal.
//
// The forward-compatibility cost is real but small and bounded: a newer client
// asking this gateway for a scope it cannot grant now fails at creation with a
// message naming the accepted set, instead of receiving a credential that does
// not work. The message carries the valid list for the same reason a discovery
// document would — an operator who typed "readonly" should not have to guess
// which spelling was meant.
func ValidateScopes(scopes []string) error {
	for _, scope := range scopes {
		if !slices.Contains(validScopes, scope) {
			return fmt.Errorf("unknown scope %q: valid scopes are %s", scope, strings.Join(validScopes, ", "))
		}
	}
	return nil
}

// KeyIsUsable reports whether k can currently authenticate at all: active, not
// revoked, and not past its expiry. Both ValidateKey implementations below and
// the session liveness re-check in middleware.go call this so the two paths
// cannot drift on what "usable" means.
func KeyIsUsable(k *APIKey) bool {
	if k == nil || !k.Active || k.RevokedAt != nil {
		return false
	}
	return k.ExpiresAt == nil || !time.Now().After(*k.ExpiresAt)
}

// IsUsableAdmin reports whether k can currently authenticate an admin request:
// usable as a credential at all, and carrying the admin scope.
//
// It shares KeyIsUsable's expiry check rather than consulting Active alone. No
// guard can stop a surviving key from expiring a second later, but a key that is
// *already* past its expiry authenticates nothing today, so counting it lets the
// last-admin guard clear the last key that still works — producing the lockout
// it exists to prevent rather than merely failing to postpone one.
func IsUsableAdmin(k *APIKey) bool {
	return KeyIsUsable(k) && HasAdminScope(k.Scopes)
}
