package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Key is one /admin/keys row. Key is masked (head8…tail4) on every read;
// the full secret appears exactly once, in the CreateKey and RotateKey
// responses, and the gateway then keeps only a hash of it.
type Key struct {
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

// KeyCreateRequest is the POST /admin/keys body.
//
// Scopes is always sent explicitly. A scope-less request is NOT granted admin:
// the gateway's key store applies defaultScopes and mints a least-privilege
// read_only key. ferro sends one anyway so the scope a key carries is the
// operator's stated choice rather than whatever the server's default happens
// to be in the version it is talking to.
type KeyCreateRequest struct {
	Name      string     `json:"name"`
	Scopes    []string   `json:"scopes"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// The three states KeyState derives, and the value the gateway confirms a
// revocation with.
const (
	KeyStateActive  = "active"
	KeyStateExpired = "expired"
	KeyStateRevoked = "revoked"
)

// KeyState derives the state column the gateway does not serve: there is no
// state or revoked field on the wire, only revoked_at, expires_at and active.
//
// Expiry must be read from expires_at, never from active. The gateway clears
// active in exactly one place — Revoke, which stamps revoked_at in the same
// write, on both the memory and SQL stores — so a key that has merely expired
// is still served as active:true with revoked_at:null and a past expires_at.
// Deriving "expired" from !active therefore never fired for a real expired
// key, and reported it as active: the one answer that matters here, on a
// surface an operator reads to decide whether a credential still works.
// model.KeyIsUsable is the authority and checks all three independently.
func KeyState(k Key) string {
	switch {
	case k.RevokedAt != nil:
		return KeyStateRevoked
	case k.ExpiresAt != nil && time.Now().After(*k.ExpiresAt):
		return KeyStateExpired
	case !k.Active:
		// Not reachable from a gateway that only clears active in Revoke, but
		// if one ever serves it, the key authenticates nothing — and "revoked"
		// is the administrative reading, where "expired" would name a deadline
		// that has not passed.
		return KeyStateRevoked
	default:
		return KeyStateActive
	}
}

// Keys lists GET /admin/keys, which answers with a bare array — no {"data":…}
// envelope, unlike every other admin list.
func (c *Client) Keys(ctx context.Context) ([]Key, error) {
	var out []Key
	if _, err := c.do(ctx, http.MethodGet, "/admin/keys", nil, nil, &out, doOpts{}); err != nil {
		return nil, err
	}
	return out, nil
}

// Key reads one key by id.
func (c *Client) Key(ctx context.Context, id string) (*Key, error) {
	var out Key
	if _, err := c.do(ctx, http.MethodGet, keyPath(id), nil, nil, &out, doOpts{}); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateKey creates a key. The returned Key carries the full secret in Key,
// and this is the only response that ever will.
func (c *Client) CreateKey(ctx context.Context, req KeyCreateRequest) (*Key, error) {
	var out Key
	if _, err := c.do(ctx, http.MethodPost, "/admin/keys", nil, req, &out, doOpts{}); err != nil {
		return nil, err
	}
	return &out, nil
}

// RotateKey issues a new secret for an existing key. The previous secret stops
// authenticating immediately, and the new one is returned once.
func (c *Client) RotateKey(ctx context.Context, id string) (*Key, error) {
	var out Key
	if _, err := c.do(ctx, http.MethodPost, keyPath(id)+"/rotate", nil, nil, &out, doOpts{}); err != nil {
		return nil, err
	}
	return &out, nil
}

// RevokeKey revokes a key immediately and irreversibly. The gateway confirms
// with {"status":"revoked"}; anything else is reported as a failure rather
// than assumed to have worked.
func (c *Client) RevokeKey(ctx context.Context, id string) error {
	var out struct {
		Status string `json:"status"`
	}
	if _, err := c.do(ctx, http.MethodPost, keyPath(id)+"/revoke", nil, nil, &out, doOpts{}); err != nil {
		return err
	}
	if out.Status != KeyStateRevoked {
		return fmt.Errorf("gateway did not confirm revocation of %s (status %q)", id, out.Status)
	}
	return nil
}

// keyPath escapes the id so a caller-supplied value cannot climb out of
// /admin/keys via path traversal.
func keyPath(id string) string { return "/admin/keys/" + url.PathEscape(id) }
