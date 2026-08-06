package model

import (
	"time"

	"github.com/ferro-labs/ai-gateway/config"
)

// PersistedConfigVersion is one durable config_history record: a config snapshot
// and the version and time at which it became the active config.
type PersistedConfigVersion struct {
	Version   int
	Config    config.Config
	UpdatedAt time.Time
	// Actor is who applied this version, frozen at write time. Empty means the
	// row predates actor recording — not that nobody applied it.
	Actor string
	// RolledBackFrom names the version this one superseded by restoring an
	// earlier config. Nil for an ordinary update, which is also what a row
	// written before the column existed reports.
	RolledBackFrom *int
}
