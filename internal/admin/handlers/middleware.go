package handlers

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/ferro-labs/ai-gateway/internal/admin/model"
	"github.com/ferro-labs/ai-gateway/internal/admin/repository"
	"github.com/ferro-labs/ai-gateway/internal/redact"
)

// syntheticMasterKeyPrefix begins the credential ID NewCredentialValidator
// fabricates for the master key. It has no row in the key store, so a session
// minted from it cannot be re-checked with store.Get — see isSyntheticCredential
// and the syntheticCredentialLive func NewCredentialValidator returns for what
// stands in for that check instead.
//
// It is a prefix completed by a fingerprint of the configured key (see
// masterCredentialID), so the ID names one particular master key rather than
// the role.
const syntheticMasterKeyPrefix = "master-key:"

// isSyntheticCredential reports whether id names the fabricated master identity
// rather than a row a Store can look up. It matches on prefix: which master key
// the fingerprint names, and whether that key is still the configured one, is
// syntheticCredentialLive's question, not this one's. A stored key's ID cannot
// collide with the prefix — those are generated as hex UUIDs.
func isSyntheticCredential(id string) bool {
	return strings.HasPrefix(id, syntheticMasterKeyPrefix)
}

// masterCredentialID derives the master key's synthetic credential ID by
// completing syntheticMasterKeyPrefix with a fingerprint of the configured key.
//
// Binding the identity to the key's value is what makes rotation take effect
// immediately. A session records this ID when it is minted and the liveness
// gate re-derives it from whatever master key is configured on every later
// request, so a key that has since been changed — or removed, which derives to
// an empty ID nothing can equal — stops matching, and the sessions minted from
// the old value stop authenticating rather than running out the remaining 24h
// of their lifetime.
//
// The fingerprint is a truncated SHA-256, the primitive the key store already
// hashes a stored key with and for the same reason: a master key comes from
// crypto/rand (ferrogw init), so there is no low-entropy space a slower KDF
// would defend. It is the only representation of the master key that reaches a
// session row, an audit record, or a rate-limit bucket — the key itself never
// leaves the validator closure.
func masterCredentialID(masterKey string) string {
	if masterKey == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(masterKey))
	return syntheticMasterKeyPrefix + hex.EncodeToString(sum[:8])
}

// CredentialValidator resolves a presented bearer credential to the identity it
// authenticates, or reports false.
type CredentialValidator func(ctx context.Context, presented string) (*model.APIKey, bool)

// SyntheticCredentialLive reports whether id — a synthetic credential ID
// isSyntheticCredential recognizes — is still admissible under the same gate
// that originally granted it. It is meaningless for a real key ID (a row a
// Store can look up); callers only ever consult it after isSyntheticCredential
// has already reported true.
type SyntheticCredentialLive func(ctx context.Context, credentialID string) bool

// NewCredentialValidator builds the credential chain shared by AuthMiddleware
// and the session-exchange handler, and the matching liveness gate for a
// session minted from its synthetic master identity. Both the validator and
// the gate must accept/admit exactly the same credentials; a second copy of
// either would be an authentication bug waiting for the two to drift.
func NewCredentialValidator(store repository.Store, masterKey string) (CredentialValidator, SyntheticCredentialLive) {
	// Name matches ID: a synthetic credential has no operator-chosen name, and
	// FormatActor renders the pair as one string when they agree.
	masterID := masterCredentialID(masterKey)
	masterAPIKey := &model.APIKey{
		ID:     masterID,
		Name:   masterID,
		Scopes: []string{model.ScopeAdmin},
		Active: true,
	}

	validate := func(ctx context.Context, presented string) (*model.APIKey, bool) {
		if presented == "" {
			return nil, false
		}

		// 1. Master key check (always active if set).
		// The guard checks != "" because subtle.ConstantTimeCompare returns 1 when both
		// inputs are empty. Without this guard, an unset key plus an empty presented
		// credential would compare equal, bypassing authentication.
		if masterKey != "" && subtle.ConstantTimeCompare([]byte(presented), []byte(masterKey)) == 1 {
			return masterAPIKey, true
		}

		// 2. Key store lookup.
		return store.ValidateKey(ctx, presented)
	}

	// syntheticCredentialLive is the liveness gate for a session minted from
	// the master identity (see AuthMiddlewareWithSessions). It is admissible
	// only while the key it was derived from is the one configured now:
	// rotating the master key after a leak, or removing it, is an emergency
	// control and has to cut off the sessions minted from the old value as well
	// as the value itself. masterID is empty when no master key is configured,
	// and no credential ID can equal it.
	syntheticCredentialLive := func(_ context.Context, credentialID string) bool {
		return credentialID == masterID
	}

	return validate, syntheticCredentialLive
}

// AuthMiddleware returns a chi-compatible middleware that validates API keys
// and stores the authenticated key in the request context.
// If masterKey is non-empty, it is checked first and grants full admin scope.
func AuthMiddleware(store repository.Store, masterKey string) func(http.Handler) http.Handler {
	return AuthMiddlewareWithSessions(store, nil, masterKey)
}

// AuthMiddlewareWithSessions validates a bearer that may be either a dashboard
// session token or an API key.
//
// The two are told apart by prefix rather than by probing both stores, so an
// API key is never looked up in the session table or the reverse. A nil
// sessions store means session auth is not configured, and every bearer falls
// through to the credential chain — which is exactly the behaviour before
// sessions existed.
func AuthMiddlewareWithSessions(store repository.Store, sessions repository.SessionStore, masterKey string) func(http.Handler) http.Handler {
	validate, syntheticCredentialLive := NewCredentialValidator(store, masterKey)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			if auth == "" || !strings.HasPrefix(auth, "Bearer ") {
				writeError(w, http.StatusUnauthorized, "missing or invalid authorization header", "authentication_error", "missing_api_key")
				return
			}
			presented := strings.TrimPrefix(auth, "Bearer ")

			if sessions != nil && strings.HasPrefix(presented, model.SessionTokenPrefix) {
				sess, ok := sessions.ValidateSession(r.Context(), presented)
				if !ok {
					writeError(w, http.StatusUnauthorized, "session expired or invalid", "authentication_error", "invalid_session")
					return
				}
				// A session outlives the key it was minted from by design (up to
				// 24h), so revoking, deleting, or expiring that key must not leave
				// a live session behind — the same key store lookup is re-checked
				// on every request rather than cascading the revoke to every
				// session at mutation time, which is fail-open if any mutation
				// site is missed. The synthetic master-key identity has no row
				// in the store, so a lookup against it would reject every
				// session minted from it — it gets syntheticCredentialLive
				// instead, which re-evaluates the same gate that admitted it
				// rather than exempting it outright: the master key is valid for
				// as long as that same value stays configured, and a session
				// minted from a value since rotated away must not outlive it.
				// scopes defaults to the mint-time snapshot, which is correct for
				// a synthetic identity (admitted once, never re-scoped). For a
				// real credential it is overwritten below with the live value:
				// a de-scope of the source key must take effect on the session
				// immediately, not up to 24h later, and the store lookup already
				// below to re-check liveness has the current scopes in hand at
				// no extra cost.
				scopes := sess.Scopes
				if isSyntheticCredential(sess.CredentialID) {
					if !syntheticCredentialLive(r.Context(), sess.CredentialID) {
						writeError(w, http.StatusUnauthorized, "session expired or invalid", "authentication_error", "invalid_session")
						return
					}
				} else {
					cred, credOK := store.Get(r.Context(), sess.CredentialID)
					if !credOK || !model.KeyIsUsable(cred) {
						writeError(w, http.StatusUnauthorized, "session expired or invalid", "authentication_error", "invalid_session")
						return
					}
					scopes = cred.Scopes // store.Get already returns a clone
				}
				// A session is presented to the rest of the stack as an APIKey so
				// no downstream handler needs to know which kind of credential
				// authenticated the request. The sessionIdentityPrefix keeps this
				// synthetic ID from ever colliding with a real key ID — see
				// deleteSession, which strips the prefix to recover the session's
				// own ID. guardKeyMutation does NOT rely on this prefix: it
				// compares authctx.KeyID (the source credential's ID, the same
				// value bucketing uses), which is what lets it refuse
				// self-mutation for a session at all — comparing identity.ID
				// here would never match a real key ID, silently disabling the
				// guard for every session.
				identity := &model.APIKey{
					ID:     repository.SessionIdentityPrefix + sess.ID,
					Name:   sess.Subject,
					Scopes: scopes,
					Active: true,
				}
				// The authctx key-ID slot must carry sess.CredentialID, not
				// identity.ID: that slot is what gateway.go copies into
				// pctx.Metadata["api_key"], the bucket key budget/ratelimit
				// plugins scope limits by. Bucketing under identity.ID would
				// hand every minted session a fresh, empty bucket -- a caller
				// could reset a spend cap or an RPM limit by minting a new
				// session from the same credential. CredentialID is already
				// the right value for the synthetic master-key credential too,
				// since that is what NewCredentialValidator's identity carries
				// as its own ID.
				next.ServeHTTP(w, r.WithContext(model.StoreKeyInContext(r.Context(), identity, sess.CredentialID)))
				return
			}

			apiKey, ok := validate(r.Context(), presented)
			if !ok {
				writeError(w, http.StatusUnauthorized, "invalid or revoked API key", "authentication_error", "invalid_api_key")
				return
			}
			next.ServeHTTP(w, r.WithContext(model.StoreKeyInContext(r.Context(), apiKey, "")))
		})
	}
}

// RequireScope returns a middleware that checks whether the authenticated key
// has at least one of the required scopes.
func RequireScope(scopes ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			apiKey, ok := model.APIKeyFromContext(r.Context())
			if !ok {
				writeError(w, http.StatusUnauthorized, "authentication required", "authentication_error", "authentication_required")
				return
			}

			for _, required := range scopes {
				for _, s := range apiKey.Scopes {
					if s == required {
						next.ServeHTTP(w, r)
						return
					}
				}
			}

			writeError(w, http.StatusForbidden, "insufficient permissions", "permission_error", "insufficient_scope")
		})
	}
}

// writeError writes a unified OpenAI-compatible JSON error response:
//
//	{"error":{"message":"...","type":"...","code":"..."}}
//
// errType and code may be empty; defaults are derived from the HTTP status.
//
// message is redacted for the same reason it is in the request-path writer:
// several callers pass an underlying error through, and a store or validation
// error can quote text it was handed. Only the message value changes.
func writeError(w http.ResponseWriter, status int, message, errType, code string) {
	if errType == "" {
		errType = defaultErrType(status)
	}
	if code == "" {
		code = errType
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"message": redact.String(message),
			"type":    errType,
			"code":    code,
		},
	})
}

func defaultErrType(status int) string {
	switch {
	case status == http.StatusUnauthorized:
		return "authentication_error"
	case status == http.StatusForbidden:
		return "permission_error"
	case status == http.StatusNotFound:
		return "not_found_error"
	case status >= 400 && status < 500:
		return "invalid_request_error"
	default:
		return "server_error"
	}
}
