package api

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Session is one dashboard session. There is no device or IP field on the
// wire — Subject is the credential's name.
type Session struct {
	ID           string     `json:"id"`
	CredentialID string     `json:"credential_id"`
	Subject      string     `json:"subject"`
	Scopes       []string   `json:"scopes"`
	CreatedAt    time.Time  `json:"created_at"`
	LastSeenAt   *time.Time `json:"last_seen_at,omitempty"`
	ExpiresAt    time.Time  `json:"expires_at"`
}

// AuditEntry is one row of the audit trail.
type AuditEntry struct {
	OccurredAt time.Time `json:"occurred_at"`
	Action     string    `json:"action"`
	Actor      string    `json:"actor"`
	ActorID    string    `json:"actor_id,omitempty"`
	TargetID   string    `json:"target_id,omitempty"`
	Outcome    string    `json:"outcome"` // ok|denied|error
	Detail     string    `json:"detail,omitempty"`
	SourceIP   string    `json:"source_ip,omitempty"`
	TraceID    string    `json:"trace_id,omitempty"`
}

// AuditQuery filters GET /admin/audit. Only non-zero fields are sent —
// Outcome in particular must be ok|denied|error or the gateway answers 400,
// so an empty one is omitted rather than sent blank.
type AuditQuery struct {
	Action  string
	ActorID string
	Outcome string
	Since   time.Time
	Limit   int
	Offset  int
}

func (q AuditQuery) values() url.Values {
	v := url.Values{}
	for k, s := range map[string]string{"action": q.Action, "actor_id": q.ActorID, "outcome": q.Outcome} {
		if s != "" {
			v.Set(k, s)
		}
	}
	if !q.Since.IsZero() {
		v.Set("since", q.Since.UTC().Format(time.RFC3339))
	}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.Offset > 0 {
		v.Set("offset", strconv.Itoa(q.Offset))
	}
	return v
}

// AuditPage is one page of GET /admin/audit.
type AuditPage struct {
	Data    []AuditEntry `json:"data"`
	Summary struct {
		TotalEntries    int `json:"total_entries"`
		ReturnedEntries int `json:"returned_entries"`
	} `json:"summary"`
}

// PluginInfo is a plugin this gateway has configured.
type PluginInfo struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Enabled bool   `json:"enabled"`
}

// BuiltinPlugin is a plugin this gateway build ships. FailsOpen lives here
// only, which is why the plugins table merges the two listings by name.
type BuiltinPlugin struct {
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Summary   string   `json:"summary"`
	Settings  []string `json:"settings"`
	FailsOpen bool     `json:"fails_open"`
}

// Sessions lists active dashboard sessions, unwrapping the {"data":…}
// envelope. A gateway with sessions disabled answers 501, for which
// IsNotSupported reports true.
func (c *Client) Sessions(ctx context.Context) ([]Session, error) {
	var out struct {
		Data []Session `json:"data"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/admin/sessions", nil, nil, &out, doOpts{}); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// Audit reads a page of the audit trail.
func (c *Client) Audit(ctx context.Context, q AuditQuery) (*AuditPage, error) {
	var out AuditPage
	if _, err := c.do(ctx, http.MethodGet, "/admin/audit", q.values(), nil, &out, doOpts{}); err != nil {
		return nil, err
	}
	return &out, nil
}

// Plugins lists the configured plugins. Like /admin/keys and /admin/providers,
// this one answers with a bare array.
func (c *Client) Plugins(ctx context.Context) ([]PluginInfo, error) {
	var out []PluginInfo
	if _, err := c.do(ctx, http.MethodGet, "/admin/plugins", nil, nil, &out, doOpts{}); err != nil {
		return nil, err
	}
	return out, nil
}

// PluginCatalog lists the plugins this gateway build ships, unwrapping the
// {"data":…} envelope.
func (c *Client) PluginCatalog(ctx context.Context) ([]BuiltinPlugin, error) {
	var out struct {
		Data []BuiltinPlugin `json:"data"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/admin/plugins/catalog", nil, nil, &out, doOpts{}); err != nil {
		return nil, err
	}
	return out.Data, nil
}
