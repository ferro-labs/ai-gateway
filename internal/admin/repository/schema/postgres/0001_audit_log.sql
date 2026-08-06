-- Every administrative action the gateway applied, and every one it refused.
--
-- outcome carries 'denied' alongside 'ok' and 'error' because the refusals are
-- half the point: "someone tried to delete the last admin key and was turned
-- away" is a question this table exists to answer, and a success-only table
-- answers it with silence that reads identically to nothing having happened.
--
-- No column holds a secret. actor and actor_id are identifiers and
-- operator-chosen names, never a bearer token, and detail is redacted before
-- it is written -- see prepareAuditEntry.
--
-- actor is a frozen display string rather than a foreign key into api_keys,
-- for the reason ActorFromContext gives: the row must still name who acted
-- after that credential is deleted, which is exactly when it gets read.
CREATE TABLE IF NOT EXISTS audit_log (
	id BIGSERIAL PRIMARY KEY,
	occurred_at TIMESTAMPTZ NOT NULL,
	action TEXT NOT NULL,
	actor TEXT NOT NULL,
	actor_id TEXT NULL,
	target_id TEXT NULL,
	outcome TEXT NOT NULL,
	detail TEXT NULL,
	source_ip TEXT NULL,
	trace_id TEXT NULL
);

-- The three ways the trail is read: newest first, one actor's history, one
-- action's history. All three lead with or end in occurred_at DESC because
-- every read is a page of the most recent matching rows, never a full scan.
CREATE INDEX IF NOT EXISTS idx_audit_log_occurred_at ON audit_log (occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_log_actor_id ON audit_log (actor_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_log_action ON audit_log (action, occurred_at DESC);
