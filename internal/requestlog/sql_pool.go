package requestlog

import (
	"database/sql"
	"time"
)

const (
	sqliteMaxOpenConns      = 1
	sqliteMaxIdleConns      = 1
	postgresMaxOpenConns    = 10
	postgresMaxIdleConns    = 5
	postgresConnMaxIdleTime = 5 * time.Minute
	postgresConnMaxLifetime = 30 * time.Minute
)

func tuneDBPool(db *sql.DB, dialect string) {
	switch dialect {
	case requestLogDialectSQLite:
		db.SetMaxOpenConns(sqliteMaxOpenConns)
		db.SetMaxIdleConns(sqliteMaxIdleConns)
		db.SetConnMaxIdleTime(0)
		db.SetConnMaxLifetime(0)
	case requestLogDialectPostgres:
		db.SetMaxOpenConns(postgresMaxOpenConns)
		db.SetMaxIdleConns(postgresMaxIdleConns)
		db.SetConnMaxIdleTime(postgresConnMaxIdleTime)
		db.SetConnMaxLifetime(postgresConnMaxLifetime)
	}
}
