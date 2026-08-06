package repository

import (
	"context"
	"database/sql"

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
// executing it. Version 2 adds config_history, the durable audit trail that Save
// writes in the same transaction as the active-config upsert. Version 3 records
// who made each change; until it existed the trail said what and when but never
// by whom. Version 4 records that a change was a rollback and what it replaced;
// until it existed a rollback was indistinguishable from an ordinary update on
// any durable backend, which is the distinction the trail is read for.
func configStoreSteps(dialect sqldb.Dialect) []migrations.Step {
	return []migrations.Step{
		{Version: 1, Name: "gateway_config_baseline", SQL: configBaselineDDL(dialect)},
		{Version: 2, Name: "config_history", SQL: configHistoryDDL(dialect)},
		{Version: 3, Name: "config_history_actor", Fn: addConfigHistoryActor(dialect)},
		{Version: 4, Name: "config_history_rolled_back_from", Fn: addConfigHistoryRollbackFrom(dialect)},
	}
}

func configBaselineDDL(dialect sqldb.Dialect) string {
	return mustSchema(dialect, "0001_gateway_config_baseline")
}

func configHistoryDDL(dialect sqldb.Dialect) string {
	return mustSchema(dialect, "0002_config_history")
}

func configHistoryActorDDL(dialect sqldb.Dialect) string {
	return mustSchema(dialect, "0003_config_history_actor")
}

func configHistoryRollbackFromDDL(dialect sqldb.Dialect) string {
	return mustSchema(dialect, "0004_config_history_rolled_back_from")
}

// addConfigHistoryActor adds config_history.actor only when it is absent.
//
// The Postgres form of the DDL says ADD COLUMN IF NOT EXISTS and SQLite has no
// equivalent, so as plain SQL this step is idempotent on one backend and not on
// the other: a database left with the column added but the ledger not yet
// updated recovers on Postgres and wedges every later start on SQLite with a
// duplicate-column error. Probing first makes both backends behave the same way.
func addConfigHistoryActor(dialect sqldb.Dialect) func(context.Context, *sql.Tx) error {
	return func(ctx context.Context, tx *sql.Tx) error {
		return ensureColumn(ctx, tx, dialect, "config_history", "actor", configHistoryActorDDL(dialect))
	}
}

// addConfigHistoryRollbackFrom adds config_history.rolled_back_from only when it
// is absent, for the reason spelled out on addConfigHistoryActor: SQLite has no
// ADD COLUMN IF NOT EXISTS, so a database left with the column added and the
// ledger not yet updated would wedge every later start.
func addConfigHistoryRollbackFrom(dialect sqldb.Dialect) func(context.Context, *sql.Tx) error {
	return func(ctx context.Context, tx *sql.Tx) error {
		return ensureColumn(ctx, tx, dialect, "config_history", "rolled_back_from", configHistoryRollbackFromDDL(dialect))
	}
}
