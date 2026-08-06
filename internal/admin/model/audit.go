package model

import (
	"time"
)

// AuditOutcome is what became of an audited action.
type AuditOutcome string

// The three outcomes an audited action can have.
//
// AuditDenied is not a failure mode of the gateway — it is the gateway working.
// It exists because "someone tried to delete the last admin key and was refused"
// is precisely the question this trail is read to answer, and a store that
// recorded only successes would answer it with a silence indistinguishable from
// nobody having tried.
const (
	AuditOK     AuditOutcome = "ok"
	AuditDenied AuditOutcome = "denied"
	AuditError  AuditOutcome = "error"
)

// AuditEntry is one recorded administrative action.
//
// Nothing here is a secret and nothing here may become one: no key secret, no
// key hash, no session token, no config value, no request or response body. The
// trail is read during an incident, often by more people than hold the
// credentials it describes, so a secret written here has leaked to everyone who
// can read it. Detail is passed through the redactor on the way in (see
// prepareAuditEntry) as a backstop, not as permission to pass secrets to it.
type AuditEntry struct {
	OccurredAt time.Time `json:"occurred_at"`
	// Action is a dotted verb: "key.create", "config.rollback",
	// "session.create".
	Action string `json:"action"`
	// Actor is the display form ActorFromContext produces, e.g.
	// "Ops laptop (key-7f2)". It is frozen at write time and never refreshed:
	// the row states what the actor was called when they acted.
	Actor string `json:"actor"`
	// ActorID is the credential id alone. Actor carries a human name that is
	// neither stable nor unique, so filtering one operator's history needs this
	// separately rather than a substring match on the display string.
	ActorID string `json:"actor_id,omitempty"`
	// TargetID is what was acted on — a key id, a config version, or "*" for a
	// fleet-wide action such as signing every operator out.
	TargetID string       `json:"target_id,omitempty"`
	Outcome  AuditOutcome `json:"outcome"`
	// Detail is optional JSON describing the change. Redacted before storage.
	Detail   string `json:"detail,omitempty"`
	SourceIP string `json:"source_ip,omitempty"`
	// TraceID ties the entry to the request logs and to the OTel span for the
	// same request, which is how "what else was this operator doing" gets asked.
	TraceID string `json:"trace_id,omitempty"`
}

// AuditQuery selects a page of audit entries. A zero value is valid and returns
// the most recent defaultAuditListLimit entries.
type AuditQuery struct {
	Limit   int
	Offset  int
	Action  string
	ActorID string
	Outcome AuditOutcome
	Since   *time.Time
}

// AuditResult is a page of audit entries plus the exact number matching the
// query's filters, ignoring Limit and Offset, so a caller can page through.
type AuditResult struct {
	Data  []AuditEntry
	Total int
}
