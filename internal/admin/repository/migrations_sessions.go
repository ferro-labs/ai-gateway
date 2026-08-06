package repository

import (
	"github.com/ferro-labs/ai-gateway/internal/migrations"
	"github.com/ferro-labs/ai-gateway/internal/sqldb"
)

// sessionLedger is the session store's own migration ledger, kept distinct from
// the key and config ledgers so all three can share one physical database
// without their independent version sequences colliding.
const sessionLedger = "session_schema_migrations"

// sessionStoreSteps returns the migration sequence for the sessions database.
func sessionStoreSteps(dialect sqldb.Dialect) []migrations.Step {
	return []migrations.Step{
		{Version: 1, Name: "sessions", SQL: mustSchema(dialect, "0001_sessions")},
	}
}
