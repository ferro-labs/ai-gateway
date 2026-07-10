package admin

import (
	"github.com/ferro-labs/ai-gateway/internal/migrations"
	"github.com/ferro-labs/ai-gateway/internal/sqldb"
)

// configLedger is the config store's own migration ledger, kept distinct from
// the key store's so the two can share one physical database without their
// independent version sequences colliding in a shared ledger.
const configLedger = "config_schema_migrations"

// configStoreSteps returns the migration sequence for the gateway_config
// database.
//
// Version 1 is the pre-runner schema. Databases created before the runner
// existed already have this shape, so Run adopts it as a baseline rather than
// executing it.
func configStoreSteps(dialect sqldb.Dialect) []migrations.Step {
	return []migrations.Step{
		{Version: 1, Name: "gateway_config_baseline", SQL: configBaselineDDL(dialect)},
	}
}

func configBaselineDDL(dialect sqldb.Dialect) string {
	if dialect == sqldb.Postgres {
		return `
CREATE TABLE IF NOT EXISTS gateway_config (
	id SMALLINT PRIMARY KEY,
	config_json TEXT NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL
);`
	}
	return `
CREATE TABLE IF NOT EXISTS gateway_config (
	id INTEGER PRIMARY KEY,
	config_json TEXT NOT NULL,
	updated_at TIMESTAMP NOT NULL
);`
}
