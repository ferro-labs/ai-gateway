package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// The three values StatusReport.State takes. They are part of the frozen
// `ferro status --format json` contract, so consumers match on these strings
// rather than on a Go type.
const (
	StateConnected   = "connected"
	StateDegraded    = "degraded"
	StateUnreachable = "unreachable"
)

// The values StatusReport.Auth takes: the two scopes a gateway credential can
// carry, and the marker for one the gateway refused. Part of the same frozen
// contract as the states above.
const (
	ScopeAdmin       = "admin"
	ScopeReadOnly    = "read_only"
	AuthUnauthorized = "unauthorized"
)

// TargetSummary counts configured routing targets and how many are reachable.
type TargetSummary struct {
	Total    int `json:"total"`
	Routable int `json:"routable"`
}

// CircuitSummary counts providers whose breaker is not closed.
type CircuitSummary struct {
	Open     int `json:"open"`
	HalfOpen int `json:"half_open"`
}

// MCPSummary counts MCP tool servers and how many completed their handshake.
type MCPSummary struct {
	Ready int `json:"ready"`
	Total int `json:"total"`
}

// StatusReport is the frozen `ferro status --format json` contract and the
// shape the TUI header renders. Adding a field is compatible; renaming or
// removing one is not.
type StatusReport struct {
	State     string `json:"state"` // connected|degraded|unreachable
	URL       string `json:"url"`
	LatencyMs int64  `json:"latency_ms"`
	// Providers and Models are nil when the gateway did not tell us, for the
	// same reason Targets is: each is populated only from a call that can
	// fail on its own. /health failing leaves a reachable, degraded gateway
	// reporting no provider count, and /v1/models is skipped entirely without
	// a credential or answered 404 by a build that does not serve it. A real
	// zero and an unanswered question are different facts, and only a pointer
	// can carry both.
	Providers *int `json:"providers,omitempty"`
	Models    *int `json:"models,omitempty"`
	// Targets is nil when the gateway did not tell us. /readyz answers a 503
	// with only {status, reason} — no targets array — so a zero count there
	// would mean "none configured" when the truth is "yours are dead", which
	// is the wrong answer during exactly the outage this command is run in.
	// Absent is honest; 0 is a claim we cannot support.
	Targets  *TargetSummary `json:"targets,omitempty"`
	Circuits CircuitSummary `json:"circuits"`
	MCP      *MCPSummary    `json:"mcp,omitempty"`
	Auth     string         `json:"auth,omitempty"` // admin|read_only|unauthorized|""
	Warnings []string       `json:"warnings,omitempty"`
}

// Status assembles the report both `ferro status` and the TUI header render.
// /health and /readyz are unauthenticated ground truth; admin and models
// enrich it best-effort. An auth problem degrades the report, it never fails
// it — only an unreachable gateway returns an error, and it returns a report
// alongside so callers can still say which URL was tried.
func (c *Client) Status(ctx context.Context) (*StatusReport, error) {
	r := &StatusReport{State: StateUnreachable, URL: c.BaseURL()}

	start := time.Now()
	health, healthStatus, err := c.Health(ctx)
	r.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		return r, err
	}
	r.State = StateConnected
	applyHealth(r, health, healthStatus)

	readyStatus := 0
	ready, status, readyErr := c.Ready(ctx)
	if readyErr == nil {
		readyStatus = status
		applyReady(r, ready, status)
	} else {
		r.Warnings = append(r.Warnings, "readiness unavailable: "+readyErr.Error())
	}

	if c.applyAuth(ctx, r) {
		if models, err := c.Models(ctx); err == nil {
			r.Models = Count(len(models))
		} else if !IsNotSupported(err) {
			r.Warnings = append(r.Warnings, "models unavailable: "+err.Error())
		}
	}

	unroutable := r.Targets != nil && r.Targets.Routable < r.Targets.Total
	// A credential the gateway refused is a degraded gateway from where this
	// operator sits: /health and /readyz are fine, and every authenticated API
	// is still shut. Presenting no credential at all is not a fault — nothing
	// was rejected, so that case reports unauthorized without degrading.
	credentialRejected := r.Auth == AuthUnauthorized && c.apiKey != ""
	if healthStatus == http.StatusServiceUnavailable || readyStatus == http.StatusServiceUnavailable ||
		readyErr != nil || r.Circuits.Open > 0 || r.Circuits.HalfOpen > 0 || unroutable || credentialRejected {
		r.State = StateDegraded
	}
	return r, nil
}

// Count records a count the gateway actually supplied. A nil *int in a
// StatusReport means the question went unanswered; Count(0) means the gateway
// answered zero. Callers building a report by hand — tests, fixtures — use it
// so that "answered zero" cannot be written accidentally as "never asked".
func Count(n int) *int { return &n }

// applyHealth folds the /health body into the report: the provider count, the
// per-provider circuit counts with a warning naming each non-closed one, and a
// warning carrying the body's own status when /health answered 503.
func applyHealth(r *StatusReport, health *HealthReport, status int) {
	r.Providers = Count(len(health.Providers))
	for _, p := range health.Providers {
		switch p.Circuit {
		case "open":
			r.Circuits.Open++
			r.Warnings = append(r.Warnings, fmt.Sprintf("%s circuit open", p.Name))
		case "half_open":
			r.Circuits.HalfOpen++
			r.Warnings = append(r.Warnings, fmt.Sprintf("%s circuit half-open", p.Name))
		}
	}
	if status == http.StatusServiceUnavailable {
		r.Warnings = append(r.Warnings, "health: "+health.Status)
	}
}

// applyReady folds a successfully read /readyz body into the report.
//
// A not_ready body carries no targets array at all, so only count them when the
// gateway actually listed some. Leaving Targets nil renders as "—" rather than
// asserting a zero the server never reported; the same holds for MCP.
func applyReady(r *StatusReport, ready *ReadyReport, status int) {
	if ready.Targets != nil {
		t := &TargetSummary{Total: len(ready.Targets)}
		for _, target := range ready.Targets {
			if target.Routable {
				t.Routable++
			}
		}
		r.Targets = t
	}
	if ready.MCPServers != nil {
		m := &MCPSummary{Total: len(ready.MCPServers)}
		for _, s := range ready.MCPServers {
			if s.Ready {
				m.Ready++
			}
		}
		r.MCP = m
	}
	if status == http.StatusServiceUnavailable && ready.Reason != "" {
		r.Warnings = append(r.Warnings, "not ready: "+ready.Reason)
	}
}

// applyAuth probes /admin/health and records the scope the credential carries,
// preferring admin over read_only. It reports whether the caller authenticated
// at all, which is what decides whether the model count can be enriched. A
// 401/403 is recorded as unauthorized and degrades the report — an auth problem
// is never an error here.
//
// Every other failure — a 500, a timeout, an undecodable body — is recorded as
// a warning instead. Without one the probe's outcome is indistinguishable from
// a gateway that authenticated fine and serves no models.
func (c *Client) applyAuth(ctx context.Context, r *StatusReport) bool {
	ah, err := c.AdminHealth(ctx)
	if err != nil {
		var ae *Error
		if errors.As(err, &ae) && (ae.Status == http.StatusUnauthorized || ae.Status == http.StatusForbidden) {
			r.Auth = AuthUnauthorized
			if c.apiKey != "" {
				r.Warnings = append(r.Warnings, "credential rejected: "+err.Error())
			}
			return false
		}
		r.Warnings = append(r.Warnings, "admin health unavailable: "+err.Error())
		return false
	}
	for _, scope := range ah.Scopes {
		if scope == ScopeAdmin {
			r.Auth = scope
			break
		}
		if scope == ScopeReadOnly && r.Auth == "" {
			r.Auth = scope
		}
	}
	return true
}
