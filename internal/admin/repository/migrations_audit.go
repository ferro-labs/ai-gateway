package repository

import (
	"github.com/ferro-labs/ai-gateway/internal/migrations"
	"github.com/ferro-labs/ai-gateway/internal/sqldb"
)

// auditLedger is the audit log's own migration ledger, kept distinct from the
// key, config, and session ledgers so all four can share one physical database
// without their independent version sequences colliding.
const auditLedger = "audit_schema_migrations"

// auditStoreSteps returns the migration sequence for the audit log.
//
// Version 1 creates the table and its three indexes together. They are one step
// on purpose: an audit_log without them is a table every list query scans, and
// splitting them would let a database exist in that state permanently once the
// table step was recorded.
func auditStoreSteps(dialect sqldb.Dialect) []migrations.Step {
	return []migrations.Step{
		{Version: 1, Name: "audit_log", SQL: mustSchema(dialect, "0001_audit_log")},
	}
}
